import type { ReactNode } from "react"

export function AuthCard({ children }: { children: ReactNode }) {
  return (
    <main className="flex h-dvh overflow-y-auto bg-(--window) p-(--s8) text-(length:--fs) text-(--fg)">
      <div className="m-auto flex w-85 max-w-full shrink-0 flex-col gap-(--s5) rounded-(--r-sheet) bg-(--chrome) p-(--s7) wrap-anywhere shadow-(--shadow-pop)">
        {children}
      </div>
    </main>
  )
}
