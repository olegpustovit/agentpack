import type { ReactNode } from "react"
import { Banner, Empty, Spinner } from "@/components/kit/feedback"

// Load failures stay visible above whatever the inspector shows.
export function Failures({ failures }: { failures: string[] }) {
  return (
    <>
      {failures.map((failure) => (
        <Banner key={failure} tone="danger" className="col-span-full">
          {failure}
        </Banner>
      ))}
    </>
  )
}

// Stands in for an inspector whose item is not in the library (yet).
export function MissingItem({
  loading,
  failures,
  title,
  children,
}: {
  loading: boolean
  failures: string[]
  title: string
  children: ReactNode
}) {
  return (
    <div className="flex flex-col gap-(--s4) p-(--s6)">
      <Failures failures={failures} />
      {loading ? (
        <div className="flex justify-center p-(--s7)">
          <Spinner />
        </div>
      ) : (
        <Empty title={title}>{children}</Empty>
      )}
    </div>
  )
}
