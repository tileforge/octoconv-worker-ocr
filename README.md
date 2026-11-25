# octoconv-worker-ocr

OCR worker for OctoConv. One process serves one approved manifest over Worker Protocol v2.

The process reads `OCR_PROTOCOL_CORE_URL`, `OCR_PROTOCOL_CLIENT_CERT`, `OCR_PROTOCOL_CLIENT_KEY`, `OCR_PROTOCOL_CLIENT_CA`, `OCR_PROTOCOL_WORKER_ID`, and `OCR_PROTOCOL_MANIFEST_PATH`. It refuses to start when `DATABASE_URL` or `REDIS_ADDR` is set. `OCR_WORKER_CONCURRENCY` must be unset or `1`. Hello and pull advertise one slot, and one conversion runs at a time.

`schema/manifest-ocr-convert.json` is capability `octoconv.convert.ocr`. It names the fifteen pairs: sources jpg, png, tiff, webp, and pdf, each to txt, hocr, and alto. `schema/manifest-ocr-render.json` is capability `octoconv.render.ocr` version 1, in a separate file, and is not approved. Sources are jpg, png, tiff, webp, and pdf, each to png, and `parameter_schema` is empty. Render uses pdftoppm for the page picture and does not use tesseract to choose or draw the page. A protocol assignment has no size and draws 1280. Direct Convert accepts only size from 256 through 2048.

`tesseract`, `pdfinfo`, `pdftoppm`, and `pdftotext` must be on `PATH`. `OCR_THREADS`, when set to a positive integer, sets `OMP_THREAD_LIMIT` for tesseract. `TESSDATA_PREFIX` is passed through when it is set. The default engine budget is 744s (`OCR_ENGINE_TIMEOUT`). `OCR_MAX_PAGES` defaults to 200. `OCR_MAX_IMAGE_PIXELS` defaults to 100000000. Landlock confines the four programs to the job directory. `OCR_SANDBOX_DISABLED=true` turns that off. A sandbox setup failure exits 78. The manifest is not approved.

`language` is the only parameter. An empty parameters object keeps `-l eng`. An unknown key fails before any program runs.
