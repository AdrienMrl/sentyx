import { GoogleGenAI, Type } from "@google/genai";
import "dotenv/config";

const apiKey = process.env.GEMINI_API_KEY;
if (!apiKey) {
  throw new Error("GEMINI_API_KEY is not set");
}

// Usage: tsx analyze-video.ts [--json] <video-path>
// With --json, stdout carries exactly one JSON verdict object (progress goes
// to stderr) so a caller like teslcam-collect can parse it.
const args = process.argv.slice(2);
const jsonMode = args.includes("--json");
const videoPath = args.find((a) => a !== "--json");
if (!videoPath) {
  throw new Error("Usage: tsx analyze-video.ts [--json] <video-path>");
}
const log = jsonMode ? console.error : console.log;

const ai = new GoogleGenAI({ apiKey });

const MODEL = "gemini-3.5-flash";

const PROMPT = `You are a security analyst reviewing footage from a Tesla vehicle's
Sentry Mode / TeslaCam system. This camera activates when the parked car detects a
potential threat nearby.

Watch the video carefully and report any nefarious, threatening, or concerning
activity directed at the vehicle or its surroundings. Consider things like:
- Someone touching, hitting, kicking, keying, or otherwise damaging the vehicle
- Attempted break-in, theft, or tampering (door handles, windows, wheels, charge port)
- A person loitering, casing the vehicle, or behaving suspiciously
- Vandalism, weapons, or violence
- Vehicle collisions or hit-and-run

Respond in structured form:
1. CONCERN DETECTED: yes / no
2. THREAT LEVEL: none / low / medium / high
3. WHAT HAPPENED: a factual description of the events, with approximate timestamps
4. EVIDENCE: the specific visual cues that support your assessment
5. RECOMMENDED ACTION: what the owner should do (e.g., ignore, review, report to police)

Be precise and avoid speculation beyond what is visible.`;

const VERDICT_SCHEMA = {
  type: Type.OBJECT,
  properties: {
    concern_detected: { type: Type.BOOLEAN },
    threat_level: {
      type: Type.STRING,
      enum: ["none", "low", "medium", "high"],
    },
    what_happened: { type: Type.STRING },
    evidence: { type: Type.STRING },
    recommended_action: { type: Type.STRING },
  },
  required: [
    "concern_detected",
    "threat_level",
    "what_happened",
    "evidence",
    "recommended_action",
  ],
};

async function main() {
  log(`Uploading ${videoPath} ...`);
  let file = await ai.files.upload({ file: videoPath! });
  log(`Uploaded: ${file.name} (state: ${file.state})`);

  // Wait for the file to finish processing before referencing it.
  while (file.state === "PROCESSING") {
    await new Promise((r) => setTimeout(r, 2000));
    if (!file.name) throw new Error("File has no name to poll");
    file = await ai.files.get({ name: file.name });
    log(`  ...state: ${file.state}`);
  }

  if (file.state === "FAILED") {
    throw new Error(`File processing failed: ${JSON.stringify(file.error)}`);
  }

  if (!file.uri || !file.mimeType) {
    throw new Error("Uploaded file is missing uri/mimeType");
  }

  log(`Analyzing with ${MODEL} ...\n`);
  const response = await ai.models.generateContent({
    model: MODEL,
    contents: [
      {
        role: "user",
        parts: [
          { fileData: { fileUri: file.uri, mimeType: file.mimeType } },
          { text: PROMPT },
        ],
      },
    ],
    ...(jsonMode && {
      config: {
        responseMimeType: "application/json",
        responseSchema: VERDICT_SCHEMA,
      },
    }),
  });

  if (jsonMode) {
    if (!response.text) throw new Error("Empty response from Gemini");
    const verdict = JSON.parse(response.text); // fail loudly here rather than in the caller
    const u = response.usageMetadata;
    if (!u || u.totalTokenCount === undefined) {
      throw new Error("Gemini response has no usageMetadata");
    }
    // Thinking tokens are billed at the output rate, so fold them into
    // output_tokens for cost accounting.
    verdict.usage = {
      model: MODEL,
      prompt_tokens: u.promptTokenCount ?? 0,
      output_tokens: (u.candidatesTokenCount ?? 0) + (u.thoughtsTokenCount ?? 0),
      total_tokens: u.totalTokenCount,
    };
    console.log(JSON.stringify(verdict));
  } else {
    console.log("===== GEMINI ANALYSIS =====\n");
    console.log(response.text);
  }

  log("\n===== TOKEN USAGE =====");
  log(JSON.stringify(response.usageMetadata, null, 2));
}

main();
