// SPDX-License-Identifier: MIT
import { useEffect, useState } from "react";

export function Login({ onLogin, onQuickDemo }: { onLogin: (user: string) => void; onQuickDemo: () => void }) {
  const [users, setUsers] = useState<string[]>([]);
  const [error, setError] = useState<string>();
  useEffect(() => {
    fetch("/api/users")
      .then((r) => (r.ok ? (r.json() as Promise<string[]>) : Promise.reject(new Error(`users: ${r.status}`))))
      .then(setUsers)
      .catch((e: Error) => setError(e.message));
  }, []);
  return (
    <div className="flex h-full items-center justify-center">
      <div className="w-80 rounded border border-neutral-800 bg-neutral-900 p-6">
        <h1 className="mb-1 text-lg font-semibold">FXP Console</h1>
        <p className="mb-4 text-sm text-neutral-400">Local demo. Pick a demo user to connect.</p>
        {error && <p className="mb-3 text-sm text-red-400">{error}</p>}
        <div className="flex flex-col gap-2">
          <button data-testid="quick-demo" onClick={onQuickDemo} className="rounded bg-blue-700 px-3 py-2 font-medium hover:bg-blue-600 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sky-400">Quick demo</button>
          {users.filter((u) => u !== "market-maker").map((u) => (
            <button
              key={u}
              data-testid={`login-${u}`}
              onClick={() => onLogin(u)}
              className="rounded bg-neutral-800 px-3 py-2 text-left hover:bg-neutral-700"
            >
              {u}
            </button>
          ))}
        </div>
      </div>
    </div>
  );
}
