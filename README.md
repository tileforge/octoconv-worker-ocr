# octoconv-worker-ocr

OCR worker for OctoConv. One process serves one approved manifest over Worker Protocol v1.

The process reads `OCR_PROTOCOL_CORE_URL`, `OCR_PROTOCOL_CLIENT_CERT`, `OCR_PROTOCOL_CLIENT_KEY`, `OCR_PROTOCOL_CLIENT_CA`, `OCR_PROTOCOL_WORKER_ID`, and `OCR_PROTOCOL_MANIFEST_PATH`. It refuses to start when `DATABASE_URL` or `REDIS_ADDR` is set. `OCR_WORKER_CONCURRENCY` must be unset or `1`. Hello and pull advertise one slot, and one conversion runs at a time.

`schema/manifest-ocr-convert.json` is capability `octoconv.convert.ocr`. It names the fifteen pairs: sources jpg, png, tiff, webp, and pdf, each to txt, hocr, and alto.

`tesseract`, `pdfinfo`, `pdftoppm`, and `pdftotext` must be on `PATH`. `OCR_THREADS`, when set to a positive integer, sets `OMP_THREAD_LIMIT` for tesseract. `TESSDATA_PREFIX` is passed through when it is set. The default engine budget is 744s (`OCR_ENGINE_TIMEOUT`). `OCR_MAX_PAGES` defaults to 200. `OCR_MAX_IMAGE_PIXELS` defaults to 100000000. This process does not apply Landlock. The in-repo ocr worker does. Do not move ocr jobs onto this process until that confinement is measured.

`language` is not accepted. A non-empty options map fails before any program runs. An empty options map passes `-l eng`.
