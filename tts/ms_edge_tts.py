#!/usr/bin/env python3
"""VocabLock TTS provider: Microsoft Edge TTS (edge-tts).

Protocol:
  --voice {voice} --text {text} --write-media {output}
  Exit 0 = success, non-zero = error.
"""

import argparse
import asyncio
import sys


async def synthesize(voice: str, text: str, output: str) -> None:
    import edge_tts  # pip install edge-tts
    communicate = edge_tts.Communicate(text, voice)
    await communicate.save(output)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--voice", required=True)
    parser.add_argument("--text", required=True)
    parser.add_argument("--write-media", required=True, dest="write_media")
    args = parser.parse_args()

    try:
        asyncio.run(synthesize(args.voice, args.text, args.write_media))
    except ImportError:
        print("edge-tts not installed. Run: pip install edge-tts", file=sys.stderr)
        sys.exit(2)
    except Exception as e:
        print(f"TTS error: {e}", file=sys.stderr)
        sys.exit(3)


if __name__ == "__main__":
    main()
