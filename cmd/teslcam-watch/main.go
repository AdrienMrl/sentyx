// teslcam-watch is the live-reader daemon: it watches a raw exFAT image
// out-of-band (while the car/host has it mounted and is writing) and logs
// filesystem events, flagging new Sentry events as they appear.
//
// With -copy-to and -copy-prefix, stable files under the prefix are extracted
// to a local directory as they complete (the server's input):
//
//	teslcam-watch -image /var/lib/teslcam/backing.img -interval 2s -stable-polls 3 \
//	  -copy-to /var/lib/teslcam/clips -copy-prefix /TeslaCam/SentryClips
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/AdrienMrl/teslcam/internal/pipeline"
	"github.com/AdrienMrl/teslcam/internal/tokenfile"
	"github.com/AdrienMrl/teslcam/internal/videocompress"
)

func main() {
	imagePath := flag.String("image", "", "path to the raw exFAT backing image")
	interval := flag.Duration("interval", 0, "poll interval, e.g. 2s")
	stablePolls := flag.Int("stable-polls", 0, "consecutive unchanged polls before a file is considered complete")
	copyTo := flag.String("copy-to", "", "extract stable files into this local directory (requires -copy-prefix)")
	copyPrefix := flag.String("copy-prefix", "", "only extract files under this image path, e.g. /TeslaCam/SentryClips (requires -copy-to)")
	postTo := flag.String("post-to", "", "push extracted files to this server base URL, e.g. http://127.0.0.1:8090 (requires -copy-to as the local spool)")
	tokenFile := flag.String("token-file", "", "file holding the server's bearer token; empty = no auth")
	compressVideo := flag.Bool("compress-video", true, "compress suitable H.264 MP4s before upload")
	videoRatio := flag.Float64("video-target-ratio", videocompress.DefaultTargetRatio, "target fraction of the source video bitrate")
	videoMinMB := flag.Int64("video-min-mb", videocompress.DefaultMinInputBytes>>20, "only compress videos at least this many MiB")
	videoMinKbps := flag.Int64("video-min-kbps", videocompress.DefaultMinBitrate/1000, "minimum compressed video bitrate")
	videoMaxKbps := flag.Int64("video-max-kbps", videocompress.DefaultMaxBitrate/1000, "maximum compressed video bitrate")
	videoMinSavings := flag.Float64("video-min-savings", videocompress.DefaultMinSavings, "minimum fractional size reduction required to use a transcode")
	videoEncoder := flag.String("video-encoder", defaultVideoEncoder(), "ffmpeg video encoder")
	ffmpegPath := flag.String("ffmpeg", "ffmpeg", "ffmpeg executable used for video compression")
	ffprobePath := flag.String("ffprobe", "ffprobe", "ffprobe executable used to inspect videos")
	flag.Parse()

	token, err := tokenfile.Read(*tokenFile)
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var videoCompression *videocompress.Config
	if *compressVideo && *postTo != "" {
		cfg := videocompress.DefaultConfig(*videoEncoder)
		cfg.FFmpegPath = *ffmpegPath
		cfg.FFprobePath = *ffprobePath
		cfg.MinInputBytes = *videoMinMB << 20
		cfg.TargetRatio = *videoRatio
		cfg.MinBitrate = *videoMinKbps * 1000
		cfg.MaxBitrate = *videoMaxKbps * 1000
		cfg.MinSavingsRatio = *videoMinSavings
		videoCompression = &cfg
	}

	err = pipeline.Run(ctx, pipeline.Config{
		ImagePath:        *imagePath,
		Interval:         *interval,
		StablePolls:      *stablePolls,
		CopyTo:           *copyTo,
		CopyPrefix:       *copyPrefix,
		PostTo:           *postTo,
		PostToken:        token,
		RetryDelay:       5 * time.Second,
		VideoCompression: videoCompression,
		Logf:             log.Printf,
	})
	if ctx.Err() != nil {
		log.Print("shutting down")
		return
	}
	if err != nil {
		log.Fatal(err)
	}
}

func defaultVideoEncoder() string {
	if runtime.GOOS == "linux" {
		return "h264_v4l2m2m"
	}
	return "libx264"
}
