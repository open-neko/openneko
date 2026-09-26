# brag-output

A 22.5s launch video for OpenNeko, made with the `/brag` skill (brag-slim workflow).

- `brag.mp4`: the video (1920×1080, 30fps, AAC audio). The poster is baked in as frame 0.
- `brag.jpg`: the poster frame.
- `share-copy.txt`: post copy.
- `brag-plan.md`: angle and storyboard.

## Re-render

The page in `work/` is a pure function of time and reuses the markup and CSS from
`apps/web/briefing-command-deck-prototype.html` (`work/proto.css` is copied from it).
It needs Node with Playwright/Chromium, Python with numpy, and an ffmpeg with libx264/aac
(for example `pip install imageio-ffmpeg numpy`).

```bash
cd work
ln -sf "$(python3 -c 'import imageio_ffmpeg as f; print(f.get_ffmpeg_exe())')" ffmpeg
python3 audio.py            # writes music.wav
node render.cjs             # writes ../brag-raw.mp4
node stills.cjs 7.2 13.5    # optional: still-<t>.png for checking frames
```

Fonts (Archivo, Manrope; SIL OFL) are vendored in `work/fonts/` because headless Chromium can't fetch them through the sandbox proxy.
