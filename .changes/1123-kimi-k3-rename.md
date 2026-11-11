---
type: changed
---

kimi-web's second chat model is renamed kimi-k2 → kimi-k3 and now rides the default slot. The k2 name came from the upstream API's slot id — the completion endpoint's own 422 enumeration [kimi k1 k1.5 k2 k1.5-thinking], re-verified live on release day — but a slot id is not a product name: the Kimi web product no longer surfaces a K2 at all (its picker shows the K3 generation), so naming a public model after the legacy slot misstated what a client is talking to. There is no k3 slot id upstream (a literal "k3" is rejected with the same 422), so kimi-k3 maps to the default `kimi` slot, which is what serves the product's current generation — the same slot kimi-latest and kimi-web already ride. The legacy k2 slot remains accepted by the API but carries no public name. Other providers' kimi-k2 models (API resellers) are untouched.
