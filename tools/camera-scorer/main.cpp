// Class-aware and class-agnostic camera scorer for the TeslaCam Pi agent.
// NanoDet itself is Apache-2.0 licensed; this program links its official NCNN
// demo implementation rather than copying it into the Go application.

#include <opencv2/imgproc.hpp>
#include <opencv2/videoio.hpp>

#include <algorithm>
#include <cmath>
#include <cstdlib>
#include <iomanip>
#include <iostream>
#include <set>
#include <stdexcept>
#include <string>
#include <vector>

#include "nanodet.h"
#include <cpu.h>

namespace {

struct Config {
    std::string model_param;
    std::string model_bin;
    std::string video;
    double offset = -1;
    double window = 12;
    double fps = 2;
    int threads = 2;
};

struct Signals {
    double objects = 0;
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
        if (arg == "--model-param") cfg.model_param = next();
        else if (arg == "--model-bin") cfg.model_bin = next();
        else if (arg == "--offset") cfg.offset = parse_number(next(), "offset");
        else if (arg == "--window") cfg.window = parse_number(next(), "window");
        else if (arg == "--fps") cfg.fps = parse_number(next(), "fps");
        else if (arg == "--threads") cfg.threads = static_cast<int>(parse_number(next(), "threads"));
        else if (!arg.empty() && arg[0] == '-') throw std::runtime_error("unknown option " + arg);
        else if (cfg.video.empty()) cfg.video = arg;
        else throw std::runtime_error("multiple video paths provided");
    }
    if (cfg.model_param.empty() || cfg.model_bin.empty() || cfg.video.empty() ||
        cfg.offset < 0 || cfg.window <= 0 || cfg.fps <= 0 || cfg.threads <= 0) {
        throw std::runtime_error("model files, video, non-negative offset, and positive window/fps/threads are required");
    }
    return cfg;
}

cv::Mat letterbox(const cv::Mat& image, int size) {
    const double scale = std::min(static_cast<double>(size) / image.cols,
                                  static_cast<double>(size) / image.rows);
    const int width = std::max(1, static_cast<int>(std::round(image.cols * scale)));
    const int height = std::max(1, static_cast<int>(std::round(image.rows * scale)));
    cv::Mat resized;
    cv::resize(image, resized, cv::Size(width, height), 0, 0, cv::INTER_AREA);
    cv::Mat output(size, size, CV_8UC3, cv::Scalar(0, 0, 0));
    resized.copyTo(output(cv::Rect((size - width) / 2, (size - height) / 2, width, height)));
    return output;
}

cv::Mat motion_frame(const cv::Mat& image) {
    cv::Mat gray, resized;
    cv::cvtColor(image, gray, cv::COLOR_BGR2GRAY);
    cv::resize(gray, resized, cv::Size(320, 240), 0, 0, cv::INTER_AREA);
    cv::GaussianBlur(resized, resized, cv::Size(5, 5), 0);
    return resized;
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

bool animal_label(int label) {
    return label >= 14 && label <= 23;
}

bool vehicle_label(int label) {
    return label >= 1 && label <= 8;
}

void add_detections(NanoDet& detector, const cv::Mat& frame, Signals& signals) {
    cv::Mat input = letterbox(frame, detector.input_size[0]);
    const auto detections = detector.detect(input, 0.25f, 0.5f);
    const double image_area = static_cast<double>(input.cols * input.rows);
    for (const auto& box : detections) {
        double class_weight = 0.70;
        if (box.label == 0 || animal_label(box.label)) class_weight = 1.0;
        else if (vehicle_label(box.label)) class_weight = 0.55;
        const double area = std::max(0.0f, box.x2 - box.x1) * std::max(0.0f, box.y2 - box.y1) / image_area;
        const double area_weight = 0.55 + 0.45 * std::min(1.0, std::sqrt(area / 0.10));
        const double score = clamp(box.score * class_weight * area_weight);
        signals.objects = std::max(signals.objects, score);
        if (box.score >= 0.35f && box.label >= 0 && box.label < static_cast<int>(detector.labels.size())) {
            signals.reasons.insert(detector.labels[box.label]);
        }
    }
}

Signals score_video(const Config& cfg) {
    cv::setNumThreads(cfg.threads);
    ncnn::set_omp_num_threads(cfg.threads);
    NanoDet detector(cfg.model_param.c_str(), cfg.model_bin.c_str(), false);

    cv::VideoCapture video(cfg.video);
    if (!video.isOpened()) throw std::runtime_error("cannot open video");
    const double duration = video.get(cv::CAP_PROP_FRAME_COUNT) /
                            std::max(1.0, video.get(cv::CAP_PROP_FPS));
    const double start = std::max(0.0, cfg.offset - cfg.window / 2.0);
    const double end = std::min(duration, cfg.offset + cfg.window / 2.0);
    if (end <= start) throw std::runtime_error("event window is outside video");

    Signals signals;
    cv::Mat first_gray, previous_gray;
    const int samples = std::max(2, static_cast<int>(std::floor((end - start) * cfg.fps)) + 1);
    for (int i = 0; i < samples; ++i) {
        const double second = std::min(end, start + i / cfg.fps);
        video.set(cv::CAP_PROP_POS_MSEC, second * 1000.0);
        cv::Mat frame;
        if (!video.read(frame) || frame.empty()) continue;

        const cv::Mat gray = motion_frame(frame);
        if (first_gray.empty()) first_gray = gray.clone();
        if (!previous_gray.empty()) {
            const Change change = measure_change(previous_gray, gray);
            signals.motion = std::max(signals.motion, change_score(change));
            signals.occlusion = std::max(signals.occlusion,
                clamp(std::max(change.changed_fraction / 0.55,
                               std::max(change.mean_delta, change.variance_drop))));
        }
        if (i > 0) {
            signals.novelty = std::max(signals.novelty, change_score(measure_change(first_gray, gray)));
        }
        previous_gray = gray.clone();

		// One detector pass every two seconds is enough for persistence while keeping
		// the class-agnostic motion stream at the requested higher sample FPS.
		if (i == 0 || i == samples - 1 || i % std::max(1, static_cast<int>(std::round(cfg.fps * 2))) == 0) {
            add_detections(detector, frame, signals);
        }
    }
    if (previous_gray.empty()) throw std::runtime_error("video yielded no frames");
    if (signals.motion >= 0.35) signals.reasons.insert("localized_motion");
    if (signals.novelty >= 0.35) signals.reasons.insert("scene_change");
    if (signals.occlusion >= 0.35) signals.reasons.insert("occlusion_or_impact");
    return signals;
}

void print_json(const Signals& signals) {
    std::cout << std::fixed << std::setprecision(6)
              << "{\"objects\":" << clamp(signals.objects)
              << ",\"motion\":" << clamp(signals.motion)
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
