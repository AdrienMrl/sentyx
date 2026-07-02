import { GoogleGenAI } from "@google/genai";
import "dotenv/config";

const apiKey = process.env.GEMINI_API_KEY;
if (!apiKey) {
  throw new Error("GEMINI_API_KEY is not set");
}

const videoPath = process.argv[2];
if (!videoPath) {
  throw new Error("Usage: tsx analyze-video.ts <video-path>");
}

const ai = new GoogleGenAI({ apiKey });

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

async function main() {
  console.log(`Uploading ${videoPath} ...`);
  let file = await ai.files.upload({ file: videoPath });
  console.log(`Uploaded: ${file.name} (state: ${file.state})`);

  // Wait for the file to finish processing before referencing it.
  while (file.state === "PROCESSING") {
    await new Promise((r) => setTimeout(r, 2000));
    if (!file.name) throw new Error("File has no name to poll");
    file = await ai.files.get({ name: file.name });
    console.log(`  ...state: ${file.state}`);
  }

  if (file.state === "FAILED") {
    throw new Error(`File processing failed: ${JSON.stringify(file.error)}`);
  }

  if (!file.uri || !file.mimeType) {
    throw new Error("Uploaded file is missing uri/mimeType");
  }

  console.log("Analyzing with gemini-3.5-flash ...\n");
  const response = await ai.models.generateContent({
    model: "gemini-3.5-flash",
    contents: [
      {
        role: "user",
        parts: [
          { fileData: { fileUri: file.uri, mimeType: file.mimeType } },
          { text: PROMPT },
        ],
      },
    ],
  });

  console.log("===== GEMINI ANALYSIS =====\n");
  console.log(response.text);

  console.log("\n===== TOKEN USAGE =====");
  console.log(JSON.stringify(response.usageMetadata, null, 2));
}

main();
