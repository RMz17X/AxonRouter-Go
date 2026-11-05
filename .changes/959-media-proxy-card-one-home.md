## Media detail page: the proxy card returns to its one home

MediaProviderDetail rendered the NoAuth proxy card for noAuth
providers. That card writes the provider's chat-egress proxy strategy —
a setting media handlers never read — and the same card already lives
on the provider's Connections detail page, which is where proxy
strategy belongs. One setting, one home: the media page no longer
renders the card (its Connections section now shows for keyed
providers only, as before), and a comment in the page records why so
it is not re-added.
