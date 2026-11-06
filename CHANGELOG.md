# Changelog

All notable changes to this project will be documented in this file.

## [v0.3.0](https://github.com/somaz94/whatdies/compare/v0.2.0...v0.3.0) (2026-11-06)

### Features

- colour and wrap check output on a terminal and re-record the demo with it ([d40be95](https://github.com/somaz94/whatdies/commit/d40be95e2166f863af01b3a5ef01e084bf8b172e))

### Bug Fixes

- skip lookups after kubectl config set and under sudo, and name the namespace a lookup used ([4933d7b](https://github.com/somaz94/whatdies/commit/4933d7b56466f472dddd81e09af472008331a897))

### Documentation

- add helm uninstall, Codex block and bucket guard scenes to the demo GIF ([fb434a0](https://github.com/somaz94/whatdies/commit/fb434a02e6622382b88e787f937a8afa53fa06da))

### Contributors

- somaz

<br/>

## [v0.2.0](https://github.com/somaz94/whatdies/compare/v0.1.1...v0.2.0) (2026-11-06)

### Features

- support Codex, where an ask becomes a deny sent on stderr and the install checks the user's shell blocks ([ea1e61a](https://github.com/somaz94/whatdies/commit/ea1e61a0f0cd17b75c803582ac050f1907a38459))
- judge helm uninstall and rollback from the release they act on and read --dry-run as helm does ([d89412f](https://github.com/somaz94/whatdies/commit/d89412fa6a6ec3a94d7da21e53d5883351e643c3))
- ask when a terraform update turns off a guard such as force_destroy or deletion_protection ([b4db6b5](https://github.com/somaz94/whatdies/commit/b4db6b519da9c87ccdd67234925d553565f5af00))
- decode saved terraform plans in process instead of running terraform show ([d41c50e](https://github.com/somaz94/whatdies/commit/d41c50ec2e237381f58f236aeebad7ddd74e0dfc))

### Tests

- track the saved plan fixtures that the tfplan ignore rule excluded ([b75bb51](https://github.com/somaz94/whatdies/commit/b75bb51a43c240120b3f862a230427bf00838004))

### Contributors

- somaz

<br/>

## [v0.1.1](https://github.com/somaz94/whatdies/compare/v0.1.0...v0.1.1) (2026-11-06)

### Bug Fixes

- judge git by its subcommand and correct comments left stale by the refactor ([7f67669](https://github.com/somaz94/whatdies/commit/7f676690499fc91f07e32c65c98006a9d4c9075a))
- close make, heredoc and flag gaps and remove false prompts found re-reviewing the bypass fixes ([6bba3df](https://github.com/somaz94/whatdies/commit/6bba3df74cb253d9380ac7a6d459b7fd1c09940f))
- refuse settings with data after the JSON object and cover the remaining shell and install branches ([b56af5a](https://github.com/somaz94/whatdies/commit/b56af5a4bd31ba3b3411abc353cd41f32ca08004))
- name oc in its own reasons and stop lookups after oc project or login ([e7dd7dd](https://github.com/somaz94/whatdies/commit/e7dd7ddbcd5c5f0da4827340632ad296871d51ae))
- clean every reason in the verdict constructors and ask on a plan change with no actions ([407551a](https://github.com/somaz94/whatdies/commit/407551a4ada2ca4ee5faa08954b9e3f43c6ab0d2))
- catch destructive commands hidden behind stdin, interpreter flags, script writes and make variables ([151b18b](https://github.com/somaz94/whatdies/commit/151b18b2eed161c325da220187ea7041f30ff3d0))
- stop the demo deleting a kind cluster it did not create ([b952d89](https://github.com/somaz94/whatdies/commit/b952d891337c677d992967eaa3af5f4020455efc))

### Code Refactoring

- separate plan facts, the hook guard and engine judges ahead of new agents and plan formats ([095711a](https://github.com/somaz94/whatdies/commit/095711af9f986bcb95c67c3792dcaeac864fcfee))

### Contributors

- somaz

<br/>

## [v0.1.0](https://github.com/somaz94/whatdies/releases/tag/v0.1.0) (2026-11-05)

### Features

- add whatdies, a pre-tool hook that judges infrastructure commands by what they destroy ([2880089](https://github.com/somaz94/whatdies/commit/28800897ea75a139def2e28179f32542d9af16e5))

### Contributors

- somaz

<br/>

