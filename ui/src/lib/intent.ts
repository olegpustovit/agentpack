import { useMutation } from "@tanstack/react-query"

// getRandomValues also works on gateways served over plain HTTP.
export function newIntentID(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(16))
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("")
}

type Intent<Input> = { input: Input; id: string }

export function useIntent<Input, Result>({
  request,
  apply,
  accepted,
  failed,
  retryID,
  scope,
}: {
  request: (input: Input, id: string) => Promise<Result>
  apply?: (input: Input, id: string) => void | (() => void)
  accepted?: (result: Result, input: Input, id: string) => void | Promise<unknown>
  scope?: { id: string }
  retryID?: (error: Error, id: string) => string
  failed: (error: Error, input: Input, retry: () => void) => void
}) {
  const mutation = useMutation({
    scope,
    mutationFn: ({ input, id }: Intent<Input>) => request(input, id),
    onMutate: ({ input, id }) => apply?.(input, id),
    onSuccess: (result, { input, id }) => accepted?.(result, input, id),
    onError: (error, intent, rollback) => {
      rollback?.()
      failed(error, intent.input, () => mutation.mutate({ ...intent, id: retryID?.(error, intent.id) ?? intent.id }))
    },
  })

  return {
    ...mutation,
    input: mutation.variables?.input,
    mutate: (input: Input, id = newIntentID(), options?: Parameters<typeof mutation.mutate>[1]) =>
      mutation.mutate({ input, id }, options),
    mutateAsync: (input: Input) => mutation.mutateAsync({ input, id: newIntentID() }),
  }
}
