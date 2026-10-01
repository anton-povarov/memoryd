"""Convert a v2 Understanding Plugin PDF request to document-text Markdown."""

import base64
import binascii
import contextlib
import hashlib
import importlib.metadata
import json
import os
import sys
import tempfile


PLUGIN_VERSION = "0.2.0"


def fail(message):
    print(f"mem-understand-pdf: {message}", file=sys.stderr)
    raise SystemExit(1)


def emit(result):
    json.dump(result, sys.stdout, ensure_ascii=False)
    sys.stdout.write("\n")


def read_request():
    try:
        request = json.load(sys.stdin)
        blob = request["blob"]
        data = base64.b64decode(blob["content_base64"], validate=True)
    except (ValueError, TypeError, KeyError, binascii.Error) as error:
        fail(f"invalid v2 request: {error}")

    if request.get("protocol_version") != 2:
        fail("unsupported protocol version")
    if blob.get("byte_size") != len(data):
        fail("Blob byte size does not match request")
    if blob.get("blobref") != "sha256-" + hashlib.sha256(data).hexdigest():
        fail("Blobref does not match request bytes")
    return blob, data


def extract_pages(data):
    # PyMuPDF4LLM may create a session file relative to the current directory.
    # Keep that file private and remove it after the conversion.
    original_directory = os.getcwd()
    with tempfile.TemporaryDirectory(prefix="memoryd-pdf-") as temporary_directory:
        os.chdir(temporary_directory)
        try:
            # Library output is diagnostic, not part of the JSON response.
            with contextlib.redirect_stdout(sys.stderr):
                import pymupdf
                import pymupdf4llm

                with pymupdf.open(stream=data, filetype="pdf") as document:
                    chunks = pymupdf4llm.to_markdown(
                        document,
                        page_chunks=True,
                        use_ocr=False,
                        write_images=False,
                        embed_images=False,
                        show_progress=False,
                    )
        finally:
            os.chdir(original_directory)
    return chunks, pymupdf.VersionBind


def main():
    blob, data = read_request()
    result = {
        "protocol_version": 2,
        "plugin_version": PLUGIN_VERSION,
        "artifacts": [],
        "warnings": [],
    }
    if blob.get("media_type") != "application/pdf" or b"%PDF-" not in data[:1024]:
        result["warnings"].append("Unsupported input: expected a PDF Blob.")
        emit(result)
        return

    try:
        chunks, pymupdf_version = extract_pages(data)
    except Exception as error:
        fail(f"PDF Markdown extraction failed: {error}")

    sections = []
    empty_pages = []
    for page_number, chunk in enumerate(chunks, 1):
        markdown = chunk["text"].strip()
        if not markdown:
            empty_pages.append(page_number)
            continue
        sections.append(f"## Page {page_number}\n\n{markdown}")
    if empty_pages:
        result["warnings"].append(
            "No extractable text on page(s): " + ", ".join(map(str, empty_pages)) + "."
        )
    if sections:
        result["artifacts"].append({
            "content_type": "text/markdown",
            "content": "\n\n".join(sections) + "\n",
            "provenance": {
                "method": "PDF layout analysis and Markdown extraction",
                "tool": "PyMuPDF4LLM",
                "tool_version": importlib.metadata.version("pymupdf4llm"),
                "pymupdf_version": pymupdf_version,
            },
        })
    else:
        result["warnings"].append("PDF has no usable embedded text; OCR was not attempted.")
    emit(result)


if __name__ == "__main__":
    main()
