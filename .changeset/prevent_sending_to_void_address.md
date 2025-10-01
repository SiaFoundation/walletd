---
default: minor
---

# Adds an `allowVoid` query parameter to [POST] /txpool/broadcast to guard against accidental burns.

By default, transactions sent to the void (zero) address are rejected. Integrators must explicitly set allowVoid=true to broadcast to the void. This prevents cases where address parsing errors (e.g. ignoring the error from UnmarshalText and falling back to the zero address) would unintentionally destroy funds.
