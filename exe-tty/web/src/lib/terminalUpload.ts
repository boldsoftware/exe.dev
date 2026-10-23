// Helper for uploading files through the terminal origin's
// POST /terminal/upload endpoint (see API.md).
// Kept out of the Vue component so it can be unit-tested in isolation.

export interface UploadResult {
  path: string;
}

// ProgressFn receives an upload fraction in [0,1] as bytes are sent.
export type ProgressFn = (fraction: number) => void;

// uploadFile POSTs a single File to the same-origin /terminal/upload endpoint
// using the raw transport (?filename=, body is the bytes). On success it
// resolves with the absolute server-side path the server wrote. On failure it
// throws an Error with a user-facing message.
//
// dir is the destination directory on the server. Empty means the server default
// (/tmp); a leading ~ expands to the login's home, a relative path is relative
// to home, and an absolute path is used as-is.
//
// When onProgress is supplied we use XMLHttpRequest (the only browser API that
// exposes upload progress) so callers can render a progress bar; otherwise we
// fall back to fetch. fetchImpl is injected for unit tests.
export async function uploadFile(
  origin: string,
  file: File,
  dir = "",
  fetchImpl: typeof fetch = fetch,
  onProgress?: ProgressFn,
): Promise<UploadResult> {
  let url = `${origin}/terminal/upload?filename=${encodeURIComponent(file.name)}`;
  if (dir.trim() !== "") url += `&dir=${encodeURIComponent(dir.trim())}`;
  const contentType = file.type || "application/octet-stream";

  if (onProgress && typeof XMLHttpRequest !== "undefined") {
    return uploadViaXHR(url, file, contentType, onProgress);
  }

  const resp = await fetchImpl(url, {
    method: "POST",
    headers: { "Content-Type": contentType },
    body: file,
    credentials: "same-origin",
  });
  const raw = await resp.text();
  return parseUploadResponse(resp.status, resp.ok, raw);
}

// uploadViaXHR mirrors the fetch path but reports upload progress.
function uploadViaXHR(
  url: string,
  file: File,
  contentType: string,
  onProgress: ProgressFn,
): Promise<UploadResult> {
  return new Promise<UploadResult>((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", url, true);
    // Same-origin requests already send cookies; keep this off to mirror the
    // fetch path's `credentials: 'same-origin'` semantics.
    xhr.setRequestHeader("Content-Type", contentType);
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable && e.total > 0) onProgress(e.loaded / e.total);
    };
    xhr.onload = () => {
      try {
        resolve(
          parseUploadResponse(xhr.status, xhr.status >= 200 && xhr.status < 300, xhr.responseText),
        );
      } catch (err) {
        reject(err);
      }
    };
    xhr.onerror = () => reject(new Error("Upload failed (network error)."));
    xhr.onabort = () => reject(new Error("Upload cancelled."));
    // No xhr.timeout: like the fetch path we don't impose a wall-clock limit,
    // since a large upload over a slow link can legitimately take a while.
    xhr.send(file);
  });
}

// parseUploadResponse turns the server's reply into a UploadResult or throws a
// user-facing Error. The body is read once as text, then parsed as the JSON
// envelope; some error paths return text/plain, which we surface as-is.
function parseUploadResponse(status: number, ok: boolean, raw: string): UploadResult {
  let body: { path?: string; error?: string } = {};
  try {
    body = JSON.parse(raw);
  } catch {
    /* non-JSON body */
  }
  if (!ok) {
    const msg =
      body.error ||
      raw.trim() ||
      (status === 413
        ? "File is larger than the upload limit."
        : `Upload failed (HTTP ${status}).`);
    throw new Error(msg);
  }
  if (!body.path) {
    throw new Error("Upload succeeded but no path was returned.");
  }
  return { path: body.path };
}
