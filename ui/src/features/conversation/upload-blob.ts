import { LOCAL } from "@/lib/api"

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
  if (!res.ok) {
    throw new Error(text || "could not upload image")
  }
  let data: { sha?: string }
  try {
    data = JSON.parse(text) as { sha?: string }
  } catch {
    throw new Error("invalid upload response")
  }
  if (!data.sha) throw new Error("invalid upload response")
  const blobUrl = new URL(`/api/sessions/${encodeURIComponent(sessionId)}/blobs/${data.sha}`, location.origin)
  if (machine && machine !== LOCAL) blobUrl.searchParams.set("node", machine)
  const bitmap = await loadImageSize(file)
  return {
    sha: data.sha,
    mime: file.type || "image/png",
    width: bitmap.width,
    height: bitmap.height,
    previewUrl: blobUrl.pathname + blobUrl.search,
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
