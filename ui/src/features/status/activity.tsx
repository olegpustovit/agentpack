import { useEffect, useState, useSyncExternalStore } from "react"
import { useQueryClient } from "@tanstack/react-query"
import { Spinner } from "@/components/kit/feedback"
import { StatusItem } from "@/components/kit/window"
import { plural } from "@/lib/format"

// Counts machines with a fetch in flight. Machine queries are keyed ["m", machine, ...].
function useFetchingMachines(): number {
  const client = useQueryClient()
  return useSyncExternalStore(
    (onChange) => client.getQueryCache().subscribe(onChange),
    () =>
      new Set(
        client
          .getQueryCache()
          .findAll({ queryKey: ["m"], fetchStatus: "fetching" })
          .map((query) => query.queryKey[1]),
      ).size,
  )
}

// Quiet at rest; shows only after 400 ms of background fetching so brief refreshes never flicker.
export function ActivityItem() {
  const fetching = useFetchingMachines()
  const busy = fetching > 0
  const [visible, setVisible] = useState(false)

  useEffect(() => {
    if (!busy) {
      setVisible(false)
      return
    }
    const timer = setTimeout(() => setVisible(true), 400)
    return () => clearTimeout(timer)
  }, [busy])

  if (!busy || !visible) return null
  return (
    <StatusItem className="flex items-center gap-(--s3)">
      <Spinner />
      <span>Updating {plural(fetching, "machine")}</span>
    </StatusItem>
  )
}
