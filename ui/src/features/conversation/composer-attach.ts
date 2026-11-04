import type { UserMessagePayload } from "./model"

export type PromptAttachment = { sha: string; name?: string; mime: string; width: number; height: number }
export type RetryPromptInput = {
  text: string
  attachments: PromptAttachment[]
  resourceLink?: { sha: string; name: string }
}

export function promptRequest(
  text: string,
  attachments: PromptAttachment[],
  imageSupported: boolean,
  resourceLink?: { sha: string; name: string },
) {
  if (resourceLink) return { path: "prompt", body: { text, resource_link: resourceLink } }
  if (attachments.length > 1) {
    return {
      path: "prompt/images",
      body: { text, images: attachments.map(({ sha, name, width, height }) => ({ sha, name: name ?? "", width, height })) },
    }
  }
  const image = attachments[0]
  if (!image) return { path: "prompt", body: { text } }
  return imageSupported
    ? { path: "prompt", body: { text, image: { sha: image.sha, mime: image.mime, width: image.width, height: image.height } } }
    : { path: "prompt", body: { text, resource_link: { sha: image.sha, name: image.name ?? "" } } }
}


const allowedImageTypes: Record<string, true> = {
  "image/png": true,
  "image/jpeg": true,
  "image/gif": true,
  "image/webp": true,
}

function allowedImage(file: File) {
  return file.type in allowedImageTypes
}

export function imageFilesFromList(files: FileList | File[] | null | undefined): File[] {
  return files ? Array.from(files).filter(allowedImage) : []
}

export function imageFilesFromClipboard(data: DataTransfer | null): File[] {
  if (!data) return []
  const files: File[] = []
  for (const item of data.items) {
    if (item.kind !== "file") continue
    const file = item.getAsFile()
    if (file && allowedImage(file)) files.push(file)
  }
  return files.length > 0 ? files : imageFilesFromList(data.files)
}
export function retryPromptInput(prompt: UserMessagePayload, imageSupported: boolean): RetryPromptInput | undefined {
  const text = prompt.text?.text ?? ""
  if (prompt.images?.length) {
    const attachments: PromptAttachment[] = []
    for (const image of prompt.images) {
      if (!image.blob || !image.name) return undefined
      attachments.push({
        sha: image.blob.replace(/^sha256:/i, ""),
        name: image.name,
        mime: image.mime ?? "image/png",
        width: image.width ?? 0,
        height: image.height ?? 0,
      })
    }
    return { text, attachments }
  }
  if (prompt.resource_link?.blob) {
    return prompt.resource_link.name
      ? { text, attachments: [], resourceLink: { sha: prompt.resource_link.blob.replace(/^sha256:/i, ""), name: prompt.resource_link.name } }
      : undefined
  }
  if (prompt.image?.blob) {
    return imageSupported
      ? {
          text,
          attachments: [{
            sha: prompt.image.blob.replace(/^sha256:/i, ""),
            name: prompt.image.name,
            mime: prompt.image.mime ?? "image/png",
            width: prompt.image.width ?? 0,
            height: prompt.image.height ?? 0,
          }],
        }
      : undefined
  }
  return text.trim() ? { text, attachments: [] } : undefined
}
