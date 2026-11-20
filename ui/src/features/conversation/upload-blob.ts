import { LOCAL, responseError } from "@/lib/api"

export type PendingAttachment = {
  sha: string
  mime: string
  width: number
  height: number
  previewUrl: string
}

export async function uploadConversationBlob(
  machine: string,
  sessionId: string,
  file: File,
): Promise<PendingAttachment> {
  const url = new URL(`/api/sessions/${encodeURIComponent(sessionId)}/conversation/blobs`, location.origin)
  if (machine && machine !== LOCAL) url.searchParams.set("node", machine)
  const res = await fetch(url, { method: "POST", body: file, headers: { "Content-Type": file.type || "application/octet-stream" } })
  const text = await res.text()
  // A proxy's error page (Cloudflare during a restart) must not reach the person as HTML.
  if (!res.ok) throw responseError(res, text)
  let data: { sha?: string }
  try {
    data = JSON.parse(text) as { sha?: string }
  } catch {
    throw new Error("invalid upload response")
  }
  if (!data.sha) throw new Error("invalid upload response")
  const bitmap = await loadImageSize(file)
  return {
    sha: data.sha,
    mime: file.type || "image/png",
    width: bitmap.width,
    height: bitmap.height,
    // The browser already has the bytes; a peer gateway may not know the blob's location yet.
    // Revoked by the composer on remove or send; a restored draft previews from the server instead.
    previewUrl: URL.createObjectURL(file),
  }
}

function loadImageSize(file: File): Promise<{ width: number; height: number }> {
  return new Promise((resolve, reject) => {
    const img = new Image()
    const objectUrl = URL.createObjectURL(file)
    img.onload = () => {
      resolve({ width: img.naturalWidth, height: img.naturalHeight })
      URL.revokeObjectURL(objectUrl)
    }
    img.onerror = () => {
      URL.revokeObjectURL(objectUrl)
      reject(new Error("could not read image"))
    }
    img.src = objectUrl
  })
}

export type SavedAttachment = Omit<PendingAttachment, "previewUrl"> & { name: string }

// Attachments are saved next to the text draft so switching chats keeps them. Only the blob's
// identity is stored: the bytes are already on the server.
export function saveAttachments(key: string, attachments: (PendingAttachment & { name: string })[]) {
  try {
    if (attachments.length === 0) sessionStorage.removeItem(key)
    else sessionStorage.setItem(key, JSON.stringify(attachments.map(({ sha, mime, width, height, name }) => ({ sha, mime, width, height, name }))))
  } catch {
    // Storage can be blocked; attachments then live only in memory.
  }
}

export function loadAttachments(key: string, preview: (sha: string) => string): (PendingAttachment & { name: string })[] {
  try {
    const saved = JSON.parse(sessionStorage.getItem(key) ?? "[]") as SavedAttachment[]
    if (!Array.isArray(saved)) return []
    return saved
      .filter((item) => typeof item?.sha === "string" && /^[0-9a-f]{64}$/i.test(item.sha))
      .map((item) => ({ sha: item.sha, mime: item.mime ?? "image/png", width: item.width ?? 0, height: item.height ?? 0, name: item.name ?? "image", previewUrl: preview(item.sha) }))
  } catch {
    return []
  }
}

export function revokePreview(url: string) {
  if (url.startsWith("blob:")) URL.revokeObjectURL(url)
}
