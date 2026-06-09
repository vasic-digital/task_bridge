// Command task_bridged is the long-running daemon entrypoint: the webhook
// receiver (P0 §6) plus the cron-cadence reconcile loop (operator default
// 10 min). P1 SCAFFOLD: process skeleton + flag stub only — it does NOT bind a
// port, register a webhook, or call ClickUp. The HTTP receiver (P6.1, with
// X-Signature HMAC verify) and the reconcile timer (P6.3, §11.4.103 background)
// land in P6.
//
// DECOUPLING (§11.4.28): all config (token, board IDs, DB path, cadence) is
// injected via the environment by the consuming project. The token is never
// printed (§11.4.10).
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "task_bridged: P1 scaffold — daemon (webhook receiver + cron reconcile) lands in P6. No port bound, no remote call made.")
	os.Exit(3)
}
