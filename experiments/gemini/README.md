# Gemini video-analysis experiment

Early spike for the downstream analyzer stage. Uploads a TeslaCam clip to the
Gemini Files API and asks it to flag threatening/nefarious activity.

## Setup

```sh
npm install
echo "GEMINI_API_KEY=..." > .env
```

## Usage

```sh
npx tsx analyze-video.ts <video-path>   # analyze a clip
npx tsx hello-gemini.ts                 # connectivity smoke test
```

`test-video/` holds sample Sentry Mode clips (gitignored — large binaries).
