# Contributing

This file covers monbooru and its companion repos (monloader,
monsender, mondocs, monbooru-plugins). Open issues and pull requests in the repo relevant to the change: app behavior here, downloading and site support in
[monloader](https://github.com/monbooru/monloader), the browser
extension in [monsender](https://github.com/monbooru/monsender),
documentation in [mondocs](https://github.com/monbooru/mondocs),
plugin and theme listings in
[monbooru-plugins](https://github.com/monbooru/monbooru-plugins).

## Bug reports and feature requests

Open an issue. For a bug, include the version, how you run it
(binary or Docker), steps to reproduce, what you expected, and any
relevant log lines/screenshots. For a feature, describe the problem or workflow
first, not only the solution you have in mind.

A report or request that turns into a shipped change is credited in
the changelog of the release that ships it.

## Pull requests

1. Open an issue describing what you want to change and why. For
   anything beyond a small fix, do this before writing code (it
   avoids wasted work if the change does not fit the project).
2. Push the code to a branch on your fork, then link the branch from the issue. 
3. This repo publishes squashed release snapshots, so pull requests
   are never merged through the GitHub UI. The review outcome lands
   in the issue or PR: accepted as-is, accepted in part, needs
   changes, or declined, with reasons.
4. Accepted work is applied upstream and appears here on the next
   release, at which point the PR is closed.

What review expects:

- Match the existing code style and search for a similar case before
  inventing a new pattern.
- Stdlib first. A new dependency needs a strong justification and
  should be discussed in the issue before any code is written.
- Small, focused changes review fast. Cross-cutting changes that
  touch many templates or handlers need discussion first.

Make sure you have reviewed every line of the diff yourself, and that you have carefully tested your code live.

## Auto-tagger configurations (monbooru)

Support for an ONNX tagger model is data, not Go. A model is
described by up to three JSON files:

- a catalog entry in `internal/tagger/catalog_default.json`: name,
  description, download URLs for the model and label files, default
  thresholds;
- a preprocessing profile in
  `internal/tagger/profile_default/<name>.json`, only when the
  defaults inferred from the label file are wrong (input size,
  layout, channels, normalization, padding, activation, label
  format, category scheme);
- a label dispatch table in
  `internal/tagger/dispatch_default/<name>.json`, routing
  model-specific labels into tag categories (a rule can also rename
  a label or drop it).

To propose a new model, get it working locally first: drop it under
`<model_path>/<name>/` and tune it with `tagger.json` and
`dispatch.json` sidecars, which use the same format as the shipped
defaults. Dispatch rules are edited in the app (the tagger's
Configure dialog, mappings tab), and its export tab shows both files
merged with your changes, with a Copy button - what you paste over
the matching `dispatch_default` / `profile_default` file in a PR is
exactly that. Once it works, a PR submits those files verbatim plus a
catalog entry with public download URLs. See
[Configuration](https://monbooru.github.io/mondocs/configuration.html#custom-onnx-models)
and the
[auto-tagger guide](https://monbooru.github.io/mondocs/guides/auto-tagger.html).

Edits to the shipped configurations are welcome the same way: a
better threshold, a label dispatched to the wrong category, a
missing rename. If you would rather not touch the repo, an issue
with the model link and the settings that worked for you is enough.

A model whose label-file format or category scheme the app does not
already read needs code changes; open an issue first.

## Site profiles (monloader)

Which sites monloader can map, and which sites monsender recognizes,
is defined by one JSON file per gallery-dl category in the monloader
repo, at `internal/mapping/profiles/<category>.json`: URL templates,
auth kind, rating and category mappings, per-site tag rules,
gallery-dl options.

To propose a new site or improve/fix an existing one, build the profile in
the app: add the site under Settings -> sites and tune it in the
site dialog. Monloader saves your version as
`profiles/<category>.json` next to your `monloader.toml`; it is the
exact same schema as the shipped files, so contributing is a PR in
monloader that adds
your file as `internal/mapping/profiles/<category>.json` as-is (or
an issue with the JSON pasted in). Include an example post URL that
works without an account when possible.

See [site profiles](https://monbooru.github.io/mondocs/addons/monloader/development.html)
in the monloader docs.

## Plugins and themes (monbooru-plugins)

A plugin is a program of your own that pairs with monbooru and can add buttons in the interfaces;
a theme is a folder of CSS. Neither needs a change in monbooru.
The pairing exchange, the button slots, the relay payload and the
theme variable names are (in theory) a stable contract, so the plugin can be built against
that, in any language, by anyone.

Listings live in [monbooru-plugins](https://github.com/monbooru/monbooru-plugins): PR one
row to `PLUGINS.md` or `THEMES.md` pointing at your repo. Listing is not
review. Bugs in a listed plugin belong on that plugin's tracker.

What belongs to monbooru instead is the contract itself: issues about pairing, relay calls, theme variables, proposals for new
surface...

## Credit

Contributors are credited in the changelog entry of the release that
ships their work: a thanks line linking the issue, plus a
co-authored mention on the release commit for contributed code. 

The Acknowledgements section of the monbooru README is for sustained
involvement: a contributor whose work
(reports, requests, or code) has been credited in multiple releases  across these repos gets a line there. 

## License

Contributions are accepted under the repository LICENSE.
