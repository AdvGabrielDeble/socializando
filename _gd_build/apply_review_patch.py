from pathlib import Path
import re

ROOT = Path(__file__).resolve().parent


def replace_once(text: str, old: str, new: str, label: str) -> str:
    count = text.count(old)
    if count != 1:
        raise SystemExit(f"{label}: expected exactly one match, found {count}")
    return text.replace(old, new, 1)


def regex_replace_once(text: str, pattern: str, replacement: str, label: str) -> str:
    updated, count = re.subn(pattern, replacement, text, count=1, flags=re.S)
    if count != 1:
        raise SystemExit(f"{label}: expected exactly one match, found {count}")
    return updated


main_path = ROOT / "main.go"
main = main_path.read_text(encoding="utf-8")
main = replace_once(
    main,
    'mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK); _, _ = io.WriteString(w, "ok") })',
    'mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("X-GD-Fiscal-Version", AppVersion); w.WriteHeader(http.StatusOK); _, _ = io.WriteString(w, "ok") })',
    "health version header",
)
main = replace_once(
    main,
    'mux.HandleFunc("/payments", a.payments)\n\tmux.HandleFunc("/payments/validate", a.validatePayments)',
    'mux.HandleFunc("/payments", a.paymentsV2)\n\tmux.HandleFunc("/payments/review", a.reviewPayment)\n\tmux.HandleFunc("/payments/validate", a.validatePayments)\n\tmux.HandleFunc("/payments/confirm-receita", a.confirmReceita)',
    "payments routes",
)
main = replace_once(
    main,
    'mux.HandleFunc("/export", a.exportPage)',
    'mux.HandleFunc("/export", a.exportPageV2)',
    "export route",
)
main_path.write_text(main, encoding="utf-8")

core_path = ROOT / "core.go"
core = core_path.read_text(encoding="utf-8")
core = replace_once(
    core,
    'AppVersion    = "1.0.0-mvp"',
    'AppVersion    = "1.1.1-mvp-review-live-update"',
    "app version",
)
core_path.write_text(core, encoding="utf-8")

print("review-feature-patch:ok")
