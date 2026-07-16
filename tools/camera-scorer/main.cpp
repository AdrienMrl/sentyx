// Class-agnostic pixel-change camera scorer for the TeslaCam Pi agent.
//
// Scores one camera's clip window using cheap frame-difference signals only:
// motion (localized change between consecutive samples), novelty (change
// versus the start of the window), and occlusion (large-area change, global
// brightness shift, or detail loss). There is deliberately no neural network:
// on a glovebox Pi in a hot car the CPU budget is the binding constraint, and
// the final relevance judgment happens server-side anyway.
//
// Decoding samples only H.264 keyframes. Tesla clips carry a keyframe roughly
// every half second, so this preserves ~2 samples/s while skipping the
// P-frame decode that otherwise dominates scoring time. The Pi's V4L2 M2M
// hardware decoder was measured slower AND hotter than keyframe-only software
// decode for this workload (it cannot skip non-keyframes and pays per-frame
// buffer conversion), and it rejects the front camera's 2896x1876 stream, so
// software decode is used everywhere.

#include <opencv2/imgproc.hpp>

#include <algorithm>
#include <cmath>
#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <iomanip>
#include <iostream>
#include <set>
#include <sstream>
#include <stdexcept>
#include <string>
#include <vector>

namespace {

// Analysis resolution. Frames are decoded and squashed to this fixed size by
// ffmpeg; all thresholds below are calibrated against it.
constexpr int kWidth = 320;
constexpr int kHeight = 240;

struct Config {
    std::string video;
    double offset = -1;
    double window = 12;
    int threads = 2;
};

struct Signals {
    double motion = 0;
    double novelty = 0;
    double occlusion = 0;
    std::set<std::string> reasons;
};

double clamp(double value) {
    return std::max(0.0, std::min(1.0, value));
}

double parse_number(const char* value, const char* name) {
    char* end = nullptr;
    const double parsed = std::strtod(value, &end);
    if (!end || *end != '\0') {
        throw std::runtime_error(std::string("invalid ") + name);
    }
    return parsed;
}

Config parse_args(int argc, char** argv) {
    Config cfg;
    for (int i = 1; i < argc; ++i) {
        const std::string arg = argv[i];
        auto next = [&]() -> const char* {
            if (++i >= argc) throw std::runtime_error("missing value for " + arg);
            return argv[i];
        };
        if (arg == "--offset") cfg.offset = parse_number(next(), "offset");
        else if (arg == "--window") cfg.window = parse_number(next(), "window");
        else if (arg == "--threads") cfg.threads = static_cast<int>(parse_number(next(), "threads"));
        else if (!arg.empty() && arg[0] == '-') throw std::runtime_error("unknown option " + arg);
        else if (cfg.video.empty()) cfg.video = arg;
        else throw std::runtime_error("multiple video paths provided");
    }
    if (cfg.video.empty() || cfg.offset < 0 || cfg.window <= 0 || cfg.threads <= 0) {
        throw std::runtime_error("video, non-negative offset, and positive window/threads are required");
    }
    return cfg;
}

struct Change {
    double component_fraction = 0;
    double changed_fraction = 0;
    double mean_delta = 0;
    double variance_drop = 0;
};

Change measure_change(const cv::Mat& before, const cv::Mat& after) {
    cv::Mat diff, mask;
    cv::absdiff(before, after, diff);
    cv::threshold(diff, mask, 18, 255, cv::THRESH_BINARY);
    const cv::Mat kernel = cv::getStructuringElement(cv::MORPH_ELLIPSE, cv::Size(3, 3));
    cv::morphologyEx(mask, mask, cv::MORPH_OPEN, kernel);

    Change change;
    const double pixels = static_cast<double>(mask.total());
    change.changed_fraction = cv::countNonZero(mask) / pixels;
    cv::Mat labels, stats, centroids;
    const int count = cv::connectedComponentsWithStats(mask, labels, stats, centroids, 8);
    for (int i = 1; i < count; ++i) {
        const double area = stats.at<int>(i, cv::CC_STAT_AREA) / pixels;
        if (area >= 0.00035) change.component_fraction = std::max(change.component_fraction, area);
    }

    cv::Scalar mean_before, std_before, mean_after, std_after;
    cv::meanStdDev(before, mean_before, std_before);
    cv::meanStdDev(after, mean_after, std_after);
    change.mean_delta = std::abs(mean_before[0] - mean_after[0]) / 90.0;
    if (std_before[0] > 4 && std_after[0] < std_before[0]) {
        change.variance_drop = (std_before[0] - std_after[0]) / std_before[0];
    }
    return change;
}

double change_score(const Change& change) {
    // Square roots deliberately give small localized changes useful weight:
    // a falling object need not occupy person-sized portions of the frame.
    // A component covering only 0.4% of the low-resolution motion frame is
    // already material. This is intentionally sensitive enough for a small
    // unclassified object falling onto the car.
    const double component = std::sqrt(change.component_fraction / 0.004);
    const double total = std::sqrt(change.changed_fraction / 0.05);
    return clamp(std::max(component, total));
}

std::string shell_quote(const std::string& value) {
    std::string quoted = "'";
    for (const char c : value) {
        if (c == '\'') quoted += "'\\''";
        else quoted += c;
    }
    return quoted + "'";
}

Signals score_video(const Config& cfg) {
    cv::setNumThreads(cfg.threads);
    const double start = std::max(0.0, cfg.offset - cfg.window / 2.0);
    // The window is centered on the event; when the event sits near the clip
    // start the front half is clipped rather than shifted later.
    const double duration = cfg.offset + cfg.window / 2.0 - start;

    std::ostringstream command;
    command << "ffmpeg -v error -nostdin"
            // Decode keyframes only; -ss before -i seeks without decoding.
            << " -skip_frame nokey"
            << " -ss " << std::fixed << std::setprecision(3) << start
            << " -t " << duration
            << " -i " << shell_quote(cfg.video)
            << " -vf scale=" << kWidth << ':' << kHeight
            // Without vfr, rawvideo output is CFR and every surviving
            // keyframe is duplicated ~18x to fill the source frame rate.
            << " -fps_mode vfr -f rawvideo -pix_fmt gray -";
    FILE* pipe = popen(command.str().c_str(), "r");
    if (!pipe) throw std::runtime_error("cannot start ffmpeg");

    Signals signals;
    cv::Mat first_gray, previous_gray;
    int processed = 0;
    const size_t frame_bytes = static_cast<size_t>(kWidth) * kHeight;
    std::vector<uint8_t> buffer(frame_bytes);
    while (std::fread(buffer.data(), 1, frame_bytes, pipe) == frame_bytes) {
        const cv::Mat raw(kHeight, kWidth, CV_8UC1, buffer.data());
        cv::Mat gray;
        cv::GaussianBlur(raw, gray, cv::Size(5, 5), 0);
        if (first_gray.empty()) first_gray = gray.clone();
        if (!previous_gray.empty()) {
            const Change change = measure_change(previous_gray, gray);
            signals.motion = std::max(signals.motion, change_score(change));
            signals.occlusion = std::max(signals.occlusion,
                clamp(std::max(change.changed_fraction / 0.55,
                               std::max(change.mean_delta, change.variance_drop))));
        }
        if (processed > 0) {
            signals.novelty = std::max(signals.novelty, change_score(measure_change(first_gray, gray)));
        }
        previous_gray = gray;
        ++processed;
    }
    const int status = pclose(pipe);
    if (status != 0) throw std::runtime_error("ffmpeg keyframe decode failed");
    // A clip shorter than the window can legitimately yield one keyframe, but
    // change signals need at least two; treat that as a scoring failure so the
    // agent's error path (select every camera) handles it.
    if (processed < 2) throw std::runtime_error("video yielded fewer than two keyframes in the event window");

    if (signals.motion >= 0.35) signals.reasons.insert("localized_motion");
    if (signals.novelty >= 0.35) signals.reasons.insert("scene_change");
    if (signals.occlusion >= 0.35) signals.reasons.insert("occlusion_or_impact");
    return signals;
}

void print_json(const Signals& signals) {
    std::cout << std::fixed << std::setprecision(6)
              << "{\"motion\":" << clamp(signals.motion)
              << ",\"novelty\":" << clamp(signals.novelty)
              << ",\"occlusion\":" << clamp(signals.occlusion)
              << ",\"reasons\":[";
    bool first = true;
    for (const auto& reason : signals.reasons) {
        if (!first) std::cout << ',';
        first = false;
        std::cout << '"' << reason << '"';
    }
    std::cout << "]}\n";
}

}  // namespace

int main(int argc, char** argv) {
    try {
        const Config cfg = parse_args(argc, argv);
        print_json(score_video(cfg));
        return 0;
    } catch (const std::exception& error) {
        std::cerr << error.what() << '\n';
        return 2;
    }
}
