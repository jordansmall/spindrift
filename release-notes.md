:robot: I have created a release *beep* *boop*
---


<details><summary>0.36.0</summary>

## [0.36.0](https://github.com/jordansmall/spindrift/compare/v0.35.0...v0.36.0) (2026-11-28)


###   BREAKING CHANGES

* **connectors:** Connector flakes no longer export flakeModules.default and core.lib.connectorModule is gone; register a Connector by adding its connectorData output to spindrift.connectorData.
* **connectors:** spindrift.connectors.<name> accepts only the shadow, shadowEvery and primary switches, on a Connector registered through spindrift.connectorData; any other field fails evaluation.
* **connectors:** a third-party Connector's backend row can no longer set `backend.credential`, and an mkHarness `backendRows` third-party row can no longer set `credentialEnv` or `firstParty`; both fail evaluation. Export the token as `<NAME>_TOKEN` instead.
* **protocol:** core refuses a forge or Driver Connector built against protocol 0.3; rebuild it against the 0.4 SDK.
* **forge:** sdk/forge's Probe moved from PRs to Branches and is required there. A forge Connector without forge.probe no longer composes.
* **protocol:** forge.remote is no longer part of the protocol and sdk/forge's Branches has no Remote field. An out-of-tree forge Connector must drop it.
* **driver:** the SDK's driver.Strategy.SessionFlags returns a driver.SessionFlag instead of a string, and the driver.sessionFlags result is {flag, value} instead of {flags}.
* **drivers:** a spindrift.connectors.<name>.driver row must declare argvShape.sessionFlags, the list of session flag names its answer may carry ([ ] for none); a row without it fails evaluation.
* **connectorforge:** a forge Connector's backend row must declare its Git remote (gitRemote or gitRemoteEnv); Compose refuses a row without one.
* **forgejo:** SPINDRIFT_FORGEJO_CONNECTOR_TOKEN and SPINDRIFT_FORGEJO_CONNECTOR_TOKEN_CMD are removed. Set FORGEJO_TOKEN (or FORGEJO_TOKEN_CMD), which now authorizes forgejo Connector requests and git.
* **jira:** a Server/Data Center site that left JIRA_EMAIL empty to get the Server dialect now gets the Cloud dialect unless it sets JIRA_DEPLOYMENT=server.
* **jira:** SPINDRIFT_JIRA_CONNECTOR_TOKEN and SPINDRIFT_JIRA_CONNECTOR_EMAIL are removed. Move their values to JIRA_TOKEN and JIRA_EMAIL, and SPINDRIFT_JIRA_CONNECTOR_TOKEN_CMD to JIRA_TOKEN_CMD.
* **launcher:** setting SPINDRIFT_GITHUB_CONNECTOR_TOKEN now fails eval and the launcher's removed-knobs check; set GH_TOKEN instead.
* **github:** SPINDRIFT_GITHUB_CONNECTOR_TOKEN is no longer read; set GH_TOKEN instead.

### Features

* **backendrows:** count a new or changed GraphQL document as a widening ([cf0b0f8](https://github.com/jordansmall/spindrift/commit/cf0b0f89433e3b5121fdaddac499cbed381cf9eb)), closes [#7566](https://github.com/jordansmall/spindrift/issues/7566)
* **backendrows:** count a newly declared Route header as a widening ([76b082b](https://github.com/jordansmall/spindrift/commit/76b082bf7057162ab35d147bc81e1e561c786f73)), closes [#7565](https://github.com/jordansmall/spindrift/issues/7565)
* **backendrows:** decode a backend row's Git remote declaration ([39a8e39](https://github.com/jordansmall/spindrift/commit/39a8e394d289aac856bd8b9e6f1cfabf18fb8307)), closes [#7508](https://github.com/jordansmall/spindrift/issues/7508)
* **backendrows:** diff a row's grant against its accepted baseline ([eadd8c9](https://github.com/jordansmall/spindrift/commit/eadd8c90a40ef389cdd368497cb798e53f6ceecc)), closes [#7563](https://github.com/jordansmall/spindrift/issues/7563)
* **backendrows:** mark a {base} Route host as knob-named ([c5c3268](https://github.com/jordansmall/spindrift/commit/c5c3268a79f753b1929cbd238980b0db90a8d972)), closes [#7567](https://github.com/jordansmall/spindrift/issues/7567)
* **box:** refuse a session answer the Driver's row does not allow ([1727ff8](https://github.com/jordansmall/spindrift/commit/1727ff89a0116e893e6820daf886fa87738ff081)), closes [#7507](https://github.com/jordansmall/spindrift/issues/7507)
* **connector-protocol:** describe the Route admit rule as a case table ([59be8f5](https://github.com/jordansmall/spindrift/commit/59be8f558aa48a03714ed298f101cf7aad87969f)), closes [#7569](https://github.com/jordansmall/spindrift/issues/7569)
* **connector/github:** send only fixed GraphQL documents and declare them ([4d4e108](https://github.com/jordansmall/spindrift/commit/4d4e108f5ab96bc35ae7988406c02301df3c99cc)), closes [#7566](https://github.com/jordansmall/spindrift/issues/7566)
* **connectorconformance:** check that declared capabilities answer ([991fc13](https://github.com/jordansmall/spindrift/commit/991fc13b44f65b5882f77b3d676d5f136c2c6a47)), closes [#7574](https://github.com/jordansmall/spindrift/issues/7574)
* **connectorforge:** accept PR URLs and run links under a subpath base ([476cc2d](https://github.com/jordansmall/spindrift/commit/476cc2d544c7a6cbd82f32d3ec36cd6c2f7f42a2)), closes [#7509](https://github.com/jordansmall/spindrift/issues/7509)
* **connectorforge:** compose a Code Forge over a supplied call ([0d09278](https://github.com/jordansmall/spindrift/commit/0d09278a53aead430aa2733ab9a7374260d86a0b)), closes [#7547](https://github.com/jordansmall/spindrift/issues/7547)
* **connectorforge:** derive the clone URL from the row's Git remote ([c2c68c7](https://github.com/jordansmall/spindrift/commit/c2c68c78baa0393a27e464dbf0766c85657a103b)), closes [#7508](https://github.com/jordansmall/spindrift/issues/7508)
* **connectorhost:** admit only allowed request and response headers ([79a4acf](https://github.com/jordansmall/spindrift/commit/79a4acf124d33c7829c75fbf2b2a9b2d870fe8a2)), closes [#7565](https://github.com/jordansmall/spindrift/issues/7565)
* **connectorhost:** let a POST Route declare GraphQL documents ([9055b82](https://github.com/jordansmall/spindrift/commit/9055b821d82c629e9a6782ae5171d59879603aae)), closes [#7566](https://github.com/jordansmall/spindrift/issues/7566)
* **connectorhost:** let a Route path segment be a one-segment * ([95572f8](https://github.com/jordansmall/spindrift/commit/95572f84fe68edab10faf46e4972d598d05aa92a)), closes [#7567](https://github.com/jordansmall/spindrift/issues/7567)
* **connectorhost:** report a dead host through Sessions.Dead ([2c0f1f5](https://github.com/jordansmall/spindrift/commit/2c0f1f55d9a7a7ec9f9f5d296f738eb8c6954b0e)), closes [#7551](https://github.com/jordansmall/spindrift/issues/7551)
* **connectorprotocol:** describe the Connector protocol as data ([2d7cebf](https://github.com/jordansmall/spindrift/commit/2d7cebf2f880406731b46f647a6d3456ce274167)), closes [#7504](https://github.com/jordansmall/spindrift/issues/7504)
* **connectorprotocol:** describe the forge axis ([e69a727](https://github.com/jordansmall/spindrift/commit/e69a72739996a07d8a4e64eb850bfddb8644432d)), closes [#7504](https://github.com/jordansmall/spindrift/issues/7504)
* **connectorprotocol:** describe the tracker axis ([e386e6b](https://github.com/jordansmall/spindrift/commit/e386e6b4ec411c64ec57456f4f809f96dc74880c)), closes [#7504](https://github.com/jordansmall/spindrift/issues/7504)
* **connectorprotocol:** describe which event kinds are progress ([bb9a25b](https://github.com/jordansmall/spindrift/commit/bb9a25b80836857ec9bec68eb8924c30a40b95e4)), closes [#7505](https://github.com/jordansmall/spindrift/issues/7505)
* **connectorprotocol:** guard the rendered protocol files ([defa523](https://github.com/jordansmall/spindrift/commit/defa523bca5048f6fb1753b6f3a4b9ec45db4025)), closes [#7504](https://github.com/jordansmall/spindrift/issues/7504)
* **connectorruntime:** add the Connector runtime ([d8d8071](https://github.com/jordansmall/spindrift/commit/d8d807132745537f3b45017c3f811ee9a04cb7fd)), closes [#7546](https://github.com/jordansmall/spindrift/issues/7546)
* **connectorruntime:** hand back Code Forges from sessions ([86ee1e9](https://github.com/jordansmall/spindrift/commit/86ee1e931f1d128528ba4ae2d55ed2e8bdfb9615)), closes [#7547](https://github.com/jordansmall/spindrift/issues/7547)
* **connectorruntime:** hand out a forge reader and the live Connector ([097484e](https://github.com/jordansmall/spindrift/commit/097484e38eff95aae1777a9aef05e23bb3aef866)), closes [#7548](https://github.com/jordansmall/spindrift/issues/7548)
* **connectorruntime:** load each backend row into its own sandbox ([288c20e](https://github.com/jordansmall/spindrift/commit/288c20e86722dd3a8ce0e9e5943fe9a2f4bcf122)), closes [#7550](https://github.com/jordansmall/spindrift/issues/7550)
* **connectorruntime:** note reloads and let a backend note its own failures ([2e3c928](https://github.com/jordansmall/spindrift/commit/2e3c9284801be87a9fa5efac53993e3864232dec)), closes [#7548](https://github.com/jordansmall/spindrift/issues/7548)
* **connectorruntime:** read each row's credential from its source ([76b9779](https://github.com/jordansmall/spindrift/commit/76b97796cb291a8edbc2db800bf1c2180e6b6b9f)), closes [#7549](https://github.com/jordansmall/spindrift/issues/7549)
* **connectorsandbox:** add a pool with a sandbox per Connector ([f3758e9](https://github.com/jordansmall/spindrift/commit/f3758e9510815a2111f88c29440d59b54fa602ef)), closes [#7550](https://github.com/jordansmall/spindrift/issues/7550)
* **connectors:** build every Connector row from its data alone ([d031c72](https://github.com/jordansmall/spindrift/commit/d031c72d0c60fefd206656b76d75bc308deb3ce4)), closes [#7562](https://github.com/jordansmall/spindrift/issues/7562)
* **connectors:** decide when a third-party row exceeds its baseline ([cdf9b08](https://github.com/jordansmall/spindrift/commit/cdf9b08c415a76c29d6831ff13a639cc0c69a189)), closes [#7564](https://github.com/jordansmall/spindrift/issues/7564)
* **connectors:** declare the headers github and forgejo send and read ([d1ef6e6](https://github.com/jordansmall/spindrift/commit/d1ef6e637e7f2c3109a2f1c8c9a23d75006ca3b7)), closes [#7565](https://github.com/jordansmall/spindrift/issues/7565)
* **connectors:** derive backend rows' first-party mark and credential ([8ae764e](https://github.com/jordansmall/spindrift/commit/8ae764e379d0aef201220c2921f847e35b09b748)), closes [#7560](https://github.com/jordansmall/spindrift/issues/7560)
* **connectors:** let a Connector register as plain data ([c549247](https://github.com/jordansmall/spindrift/commit/c5492474d4445b5296f03082df32aff8ceb03b90)), closes [#7557](https://github.com/jordansmall/spindrift/issues/7557)
* **connectors:** let a data Connector's Driver row take nixpkgs lib ([12804a5](https://github.com/jordansmall/spindrift/commit/12804a54fbf39270483f5f857bfa92cc6299896b)), closes [#7557](https://github.com/jordansmall/spindrift/issues/7557)
* **connectors:** read data-form Connectors in core's plain readers ([470ef98](https://github.com/jordansmall/spindrift/commit/470ef988f9aefeb015259d290f914dbab4d72036)), closes [#7557](https://github.com/jordansmall/spindrift/issues/7557)
* **connectors:** refuse a third-party Connector beyond its baseline ([b1cbbff](https://github.com/jordansmall/spindrift/commit/b1cbbff276e40445ca50e93889d87478e45033b5)), closes [#7564](https://github.com/jordansmall/spindrift/issues/7564)
* **connectors:** register the claude, opencode and pi Drivers as data ([64601ef](https://github.com/jordansmall/spindrift/commit/64601efe156d381cd20b44fc7c1ab4f7a70e4a86)), closes [#7559](https://github.com/jordansmall/spindrift/issues/7559)
* **connectors:** register the fixture and github Connectors as data ([5d86d44](https://github.com/jordansmall/spindrift/commit/5d86d4433d509c6e150a24ac6add834ff6c38427)), closes [#7557](https://github.com/jordansmall/spindrift/issues/7557)
* **driver:** answer driver.sessionFlags as a flag and a value ([f475053](https://github.com/jordansmall/spindrift/commit/f4750538566e44c9c1eb34b0b823771b2a9e11a8)), closes [#7507](https://github.com/jordansmall/spindrift/issues/7507)
* **drivers:** declare each Driver's session flag names on its row ([031ab59](https://github.com/jordansmall/spindrift/commit/031ab59630d0363dcfff208daff2091d5f8eb4e1)), closes [#7507](https://github.com/jordansmall/spindrift/issues/7507)
* **forgejo:** authorize the forgejo Connector with FORGEJO_TOKEN ([a47ee04](https://github.com/jordansmall/spindrift/commit/a47ee04d344317efc4e8d536acdf9ad3b3aa2e47)), closes [#7500](https://github.com/jordansmall/spindrift/issues/7500)
* **forgejo:** build the Code Forge on the Connector runtime ([d518fa2](https://github.com/jordansmall/spindrift/commit/d518fa240d7f0b4f47ba6346c77ef970f73475d5)), closes [#7547](https://github.com/jordansmall/spindrift/issues/7547)
* **forgejo:** read FORGEJO_TOKEN at each git use ([97b9601](https://github.com/jordansmall/spindrift/commit/97b96012a44396659434871fdd66b4d1aeabcbfc)), closes [#7500](https://github.com/jordansmall/spindrift/issues/7500)
* **forge:** probe a forge's liveness through forge.probe ([1c588aa](https://github.com/jordansmall/spindrift/commit/1c588aa53cbed60d6a2cb3e9d371c95bdda115ce)), closes [#7510](https://github.com/jordansmall/spindrift/issues/7510)
* **forge:** run jira, forgejo trackers on runtime ([5ce7592](https://github.com/jordansmall/spindrift/commit/5ce75925b8c79468216bc6e919414143ac26370f)), closes [#7546](https://github.com/jordansmall/spindrift/issues/7546)
* **github:** build the tracker and Code Forge on the Connector runtime ([a4110ad](https://github.com/jordansmall/spindrift/commit/a4110ad60da480c177c88deb55224c16e5a2c2df)), closes [#7135](https://github.com/jordansmall/spindrift/issues/7135)
* **github:** name GH_TOKEN as the github Connector's credential ([5008018](https://github.com/jordansmall/spindrift/commit/5008018db4540c7bc5fa62bf46ef089018a485e5)), closes [#7499](https://github.com/jordansmall/spindrift/issues/7499)
* **jira:** authorize the jira Connector with JIRA_TOKEN and JIRA_EMAIL ([2cf3e6e](https://github.com/jordansmall/spindrift/commit/2cf3e6e862df2ef4626beb193e164f6eb6e9b1f3)), closes [#7501](https://github.com/jordansmall/spindrift/issues/7501)
* **jira:** select the jira API dialect with JIRA_DEPLOYMENT ([4fc506e](https://github.com/jordansmall/spindrift/commit/4fc506eb1b8ba03980b244108a940da55446192a)), closes [#7501](https://github.com/jordansmall/spindrift/issues/7501)
* **launcher:** add one resolver for a backend row's credential ([6812c8b](https://github.com/jordansmall/spindrift/commit/6812c8b541c44f0e0301662022155cdadd45b0c1)), closes [#7503](https://github.com/jordansmall/spindrift/issues/7503)
* **launcherchecks:** require each selected backend's row credential ([9ba0367](https://github.com/jordansmall/spindrift/commit/9ba036758ef86d636b8e9534872f9f4cb24296a9)), closes [#7502](https://github.com/jordansmall/spindrift/issues/7502)
* **launcher:** refuse SPINDRIFT_GITHUB_CONNECTOR_TOKEN ([89565b1](https://github.com/jordansmall/spindrift/commit/89565b143d75a87e935b799cf7687e0ed3fd051c)), closes [#7499](https://github.com/jordansmall/spindrift/issues/7499)
* **launcher:** write the Route baseline with connectors accept ([f7b416c](https://github.com/jordansmall/spindrift/commit/f7b416cac133c2d733d18c145b9db7e735c82aae)), closes [#7563](https://github.com/jordansmall/spindrift/issues/7563)
* **nix:** declare each forge Connector's Git remote on its row ([1ac9d03](https://github.com/jordansmall/spindrift/commit/1ac9d03953f885f10f497c545a9ebe9f11ae52b8)), closes [#7508](https://github.com/jordansmall/spindrift/issues/7508)
* **nix:** fail evaluation on a never-declarable Route header ([fd472ab](https://github.com/jordansmall/spindrift/commit/fd472ab7cfb268c1032feb7c086e4943378fef35)), closes [#7565](https://github.com/jordansmall/spindrift/issues/7565)
* **nix:** let a backend row name its own backend's core identity ([8b7a39c](https://github.com/jordansmall/spindrift/commit/8b7a39ca3db2b4bb56383caef6c4cb4e99c0996c)), closes [#7501](https://github.com/jordansmall/spindrift/issues/7501)
* **nix:** let a backend row name its own backend's core token ([5054d71](https://github.com/jordansmall/spindrift/commit/5054d7175eebe8b1e8824da63ae48aaba27c48cf)), closes [#7498](https://github.com/jordansmall/spindrift/issues/7498)
* **nix:** let a removed-knobs row omit its flakePath ([467b47c](https://github.com/jordansmall/spindrift/commit/467b47caa0ec027a118b033eef1d0d16170577f9)), closes [#7498](https://github.com/jordansmall/spindrift/issues/7498)
* **nix:** read a whole-grant entry from the Route baseline ([cb1d26b](https://github.com/jordansmall/spindrift/commit/cb1d26bc93d1922eaa12358a9660020010c2cdbe)), closes [#7563](https://github.com/jordansmall/spindrift/issues/7563)
* **protocol:** remove forge.remote from the Connector protocol ([5f48d7a](https://github.com/jordansmall/spindrift/commit/5f48d7a9d72f646410fde99d56fc343c97868bb0)), closes [#7510](https://github.com/jordansmall/spindrift/issues/7510)
* **protocol:** speak Connector protocol 0.4.0 ([217fa22](https://github.com/jordansmall/spindrift/commit/217fa22ba34d238b87a1a79e491f42960c0478d7)), closes [#7510](https://github.com/jordansmall/spindrift/issues/7510)
* **quickstart:** register a chosen Connector as data ([7b0c82a](https://github.com/jordansmall/spindrift/commit/7b0c82a4b87f40bb6cc55584d6a5650d840d03ed)), closes [#7562](https://github.com/jordansmall/spindrift/issues/7562)
* **rowcredential:** choose a credential source for each backend row ([b8af9c0](https://github.com/jordansmall/spindrift/commit/b8af9c053aab7ed145335fd218bff030e56653bb)), closes [#7549](https://github.com/jordansmall/spindrift/issues/7549)
* **sandbox:** restart a dead Connector sandbox on its next use ([54337c7](https://github.com/jordansmall/spindrift/commit/54337c74d1edbd408c5ad07365c070eb673e7143)), closes [#7551](https://github.com/jordansmall/spindrift/issues/7551)
* **sdk:** add JSON send, path-segment and page-loop helpers ([7154f40](https://github.com/jordansmall/spindrift/commit/7154f40475b591d4f73d5cac830980b7ba09e6f4)), closes [#7570](https://github.com/jordansmall/spindrift/issues/7570)
* **sdk:** apply the GraphQL document rule in the fake host ([9fea7e4](https://github.com/jordansmall/spindrift/commit/9fea7e481b7bda82986881fb86115dacb5d7abe3)), closes [#7566](https://github.com/jordansmall/spindrift/issues/7566)
* **sdk:** apply the request header rule in the fake host ([ec68d48](https://github.com/jordansmall/spindrift/commit/ec68d48fc87c6446a194b7f720d1f13321326f28)), closes [#7565](https://github.com/jordansmall/spindrift/issues/7565)
* **sdk:** check that every declared capability answers ([e090781](https://github.com/jordansmall/spindrift/commit/e0907814977ab68385f7def41bce360a1ded0f3b)), closes [#7574](https://github.com/jordansmall/spindrift/issues/7574)
* **sdk:** export a fake host that applies the Route admit rule ([5a40707](https://github.com/jordansmall/spindrift/commit/5a40707f991841a1f2ca1e8b7da31bebf681e4d9)), closes [#7568](https://github.com/jordansmall/spindrift/issues/7568)
* **sdk:** export a hostile-input corpus for Connector tests ([c32f7a0](https://github.com/jordansmall/spindrift/commit/c32f7a07a7e26ade74e5a9b63f8619b2a5888a89)), closes [#7574](https://github.com/jordansmall/spindrift/issues/7574)
* **sdk:** render the SDK's protocol constants from the description ([393befc](https://github.com/jordansmall/spindrift/commit/393befc08435658d847c240e65f56500a8dcf979)), closes [#7505](https://github.com/jordansmall/spindrift/issues/7505)
* **sdk:** render the SDK's wire structs from the description ([a53f127](https://github.com/jordansmall/spindrift/commit/a53f12792eecc649f539141c71368090578f1220)), closes [#7505](https://github.com/jordansmall/spindrift/issues/7505)
* **sdk:** withhold undeclared response headers in the fake host ([1e97dfe](https://github.com/jordansmall/spindrift/commit/1e97dfeb77eef19b381d8e90213f9c838fa4e3d6)), closes [#7573](https://github.com/jordansmall/spindrift/issues/7573)
* **templates:** register the Driver template as data ([3674332](https://github.com/jordansmall/spindrift/commit/367433219b71180a5466a4a143d8b6cef2c42f7e)), closes [#7561](https://github.com/jordansmall/spindrift/issues/7561)
* **templates:** register the tracker and forge templates as data ([8b85b55](https://github.com/jordansmall/spindrift/commit/8b85b55db0f9da5833ff00ef29123a6bc6ea9434)), closes [#7561](https://github.com/jordansmall/spindrift/issues/7561)


### Bug Fixes

* **dispatch:** namespace Box names by checkout ([44151d9](https://github.com/jordansmall/spindrift/commit/44151d9e0254c9e1a55f325d13bbd22516702850)), closes [#7378](https://github.com/jordansmall/spindrift/issues/7378)
* **doctor:** name the tracker row's credential in the auth hint ([b9e7d0c](https://github.com/jordansmall/spindrift/commit/b9e7d0c3e6012247d8ba2ecced6257de2fd90e09)), closes [#7502](https://github.com/jordansmall/spindrift/issues/7502)
* **doctor:** scope the grant advisories to the rows the run reads ([83d9a1d](https://github.com/jordansmall/spindrift/commit/83d9a1d5ec2a328be1929726c815f4010f1a43b8)), closes [#7383](https://github.com/jordansmall/spindrift/issues/7383) [#7563](https://github.com/jordansmall/spindrift/issues/7563)
* **launcher:** point jira operators at spindrift.connectorData ([6e582b8](https://github.com/jordansmall/spindrift/commit/6e582b83a27da20d9359c0543f47e8412566c3db)), closes [#7562](https://github.com/jordansmall/spindrift/issues/7562)
* **seamtest:** read the fake record under a shared flock ([1621d7a](https://github.com/jordansmall/spindrift/commit/1621d7acdd5cf56d7890792a975a813e097e146c)), closes [#7640](https://github.com/jordansmall/spindrift/issues/7640)


### Performance Improvements

* **image:** build check-only fixture images uncompressed ([2fe78bc](https://github.com/jordansmall/spindrift/commit/2fe78bcde194c0a557c126adbc93bf98d8d59420)), closes [#7522](https://github.com/jordansmall/spindrift/issues/7522)


### Security

* **bwrap:** remount the Connector sandbox root read-only ([86284b7](https://github.com/jordansmall/spindrift/commit/86284b796b5907f7c952e76bd6aa05e3dd7bfc76)), closes [#7552](https://github.com/jordansmall/spindrift/issues/7552)
* **connectorhost:** address-check the redirect hop, audit its 3xx ([6575cdd](https://github.com/jordansmall/spindrift/commit/6575cdd3fe9f18b286609b0d74dff6e171009005)), closes [#7567](https://github.com/jordansmall/spindrift/issues/7567) [#6926](https://github.com/jordansmall/spindrift/issues/6926)
* **connectorhost:** close the address check's remaining gaps ([3684987](https://github.com/jordansmall/spindrift/commit/3684987032954120487d8adb9e38391cd133e9b7)), closes [#7567](https://github.com/jordansmall/spindrift/issues/7567)
* **connectorhost:** dial a literal Route host only when public ([5711625](https://github.com/jordansmall/spindrift/commit/5711625d3bc28dd18cdc81cf7a421417777e1d45)), closes [#7567](https://github.com/jordansmall/spindrift/issues/7567)
* **connectorhost:** refuse a frame for a session the pipe never issued ([99d2274](https://github.com/jordansmall/spindrift/commit/99d22746a51a2f9441609bb0b721fa9f2496bf45)), closes [#7550](https://github.com/jordansmall/spindrift/issues/7550)
* **connectorhost:** refuse a GraphQL body no declared document matches ([9312a64](https://github.com/jordansmall/spindrift/commit/9312a646c6ce1dd382012396b58b4354a458fa4c)), closes [#7566](https://github.com/jordansmall/spindrift/issues/7566)
* **connectors:** narrow the job-log Routes to a job's log ([62821a4](https://github.com/jordansmall/spindrift/commit/62821a458aa9918493da52eaf641312a805f7995)), closes [#6926](https://github.com/jordansmall/spindrift/issues/6926)
* **connectors:** refuse redeclaring the rows ([eeb1b94](https://github.com/jordansmall/spindrift/commit/eeb1b942f9a66acb9bbd5562f6f865e70d0ba6b9)), closes [#7562](https://github.com/jordansmall/spindrift/issues/7562)
* **github:** refuse a row whose gitRemote is off GH_HOST ([1be48d6](https://github.com/jordansmall/spindrift/commit/1be48d60029b46e446ab3bec86d4a5fc222d92ca)), closes [#7538](https://github.com/jordansmall/spindrift/issues/7538)
* **oci:** run the Connector sandbox pinned and never pull ([e4aeafd](https://github.com/jordansmall/spindrift/commit/e4aeafd8f876e83d2851ddb8b9e93afa32353e73)), closes [#7552](https://github.com/jordansmall/spindrift/issues/7552)


### Documentation

* **adr:** measure bwrap and podman for two and three sandboxes ([8d63c55](https://github.com/jordansmall/spindrift/commit/8d63c553d13fc771bf6624b9d1f96a0f8e296f72)), closes [#7553](https://github.com/jordansmall/spindrift/issues/7553)
* **adr:** record the Connector review's decisions ([e276feb](https://github.com/jordansmall/spindrift/commit/e276feb360027c83a73ac4c12c149d26bc8cc230))
* **adr:** record the Driver registry folded into the runtime ([fcfc0eb](https://github.com/jordansmall/spindrift/commit/fcfc0eb7dc817f4028d77e251a68534dfcb8111a)), closes [#7556](https://github.com/jordansmall/spindrift/issues/7556)
* **adr:** record what two and three Connector sandboxes cost ([b74e05e](https://github.com/jordansmall/spindrift/commit/b74e05e4f1b9f49eaccbb12ce7e6e3fcc4749e21)), closes [#7553](https://github.com/jordansmall/spindrift/issues/7553)
* **connectorprotocol:** name the registered bodies in check descriptions ([c8bae94](https://github.com/jordansmall/spindrift/commit/c8bae9446d448aac22e386460aaf45b26e47d242)), closes [#7506](https://github.com/jordansmall/spindrift/issues/7506)
* **connectors:** document the data form for Connector authors ([f0f5ce4](https://github.com/jordansmall/spindrift/commit/f0f5ce41d9c8daf7f1c90941a152685a27444bf0)), closes [#7561](https://github.com/jordansmall/spindrift/issues/7561)
* **connectors:** list forgejo and jira as data Connectors ([b65de13](https://github.com/jordansmall/spindrift/commit/b65de13a2ff7ca2317c19e14cfb7d5967d2f1530)), closes [#7558](https://github.com/jordansmall/spindrift/issues/7558)
* **connectors:** note the data registration path ([87e15d8](https://github.com/jordansmall/spindrift/commit/87e15d8beae084e1529fa0cc53738d4cf30a7c31)), closes [#7557](https://github.com/jordansmall/spindrift/issues/7557)
* correct prompt-override fragment precedence ([b2d416d](https://github.com/jordansmall/spindrift/commit/b2d416d442d97c9cc0e6521efdf51577f9b9cd7c)), closes [#7385](https://github.com/jordansmall/spindrift/issues/7385)
* describe a sandbox per credential as built ([7f4c1ee](https://github.com/jordansmall/spindrift/commit/7f4c1eeee8c220f99fc158ac618ceb9cf57f9599)), closes [#7550](https://github.com/jordansmall/spindrift/issues/7550)
* describe backend rows as held by the Connector runtime ([7ffd035](https://github.com/jordansmall/spindrift/commit/7ffd0354966de3c8e7028e4debd2bc861a5f51b0)), closes [#7555](https://github.com/jordansmall/spindrift/issues/7555)
* describe declared GraphQL documents and bound the [#7209](https://github.com/jordansmall/spindrift/issues/7209) residual ([1d53bd4](https://github.com/jordansmall/spindrift/commit/1d53bd4afed3f25ebae6c993b8c8b5083cb69034)), closes [#7566](https://github.com/jordansmall/spindrift/issues/7566)
* describe FORGEJO_TOKEN as forgejo's one credential ([4a15ae1](https://github.com/jordansmall/spindrift/commit/4a15ae113eea330183c16c1fcd53da4be61d5676)), closes [#7500](https://github.com/jordansmall/spindrift/issues/7500)
* describe GH_TOKEN as the github Connector's one credential ([77626bd](https://github.com/jordansmall/spindrift/commit/77626bd8a3327670d7e47e7f0efbfdfe06e60dfe)), closes [#7499](https://github.com/jordansmall/spindrift/issues/7499)
* describe the Connector sandbox restart ([b4c32c2](https://github.com/jordansmall/spindrift/commit/b4c32c2fe2b292b6e6f727db3a6e774f1873a1bf)), closes [#7551](https://github.com/jordansmall/spindrift/issues/7551) [#7078](https://github.com/jordansmall/spindrift/issues/7078)
* describe the launcher-go-test-fixtures split ([886052f](https://github.com/jordansmall/spindrift/commit/886052f51bd97be94576a77bbb341041f5ab6ff8)), closes [#7577](https://github.com/jordansmall/spindrift/issues/7577)
* describe the public-address rule and its proxy caveat ([785c61c](https://github.com/jordansmall/spindrift/commit/785c61ccedcf957040f7f8b3af8ae8aae1b2ea59)), closes [#7567](https://github.com/jordansmall/spindrift/issues/7567)
* describe the whole-grant baseline and connectors accept ([75eacd5](https://github.com/jordansmall/spindrift/commit/75eacd5d92595be30d58311ff8e36fca80d8f4d0)), closes [#7563](https://github.com/jordansmall/spindrift/issues/7563)
* document jira's JIRA_TOKEN, JIRA_EMAIL and JIRA_DEPLOYMENT ([02fd4bd](https://github.com/jordansmall/spindrift/commit/02fd4bdb7b0309502dc3529e948ab6a360e8aadb)), closes [#7501](https://github.com/jordansmall/spindrift/issues/7501)
* **env-schema:** say GH_TOKEN is github's credential ([c23907c](https://github.com/jordansmall/spindrift/commit/c23907c5dc477958b1370e534bf4e71e534b6c20)), closes [#7502](https://github.com/jordansmall/spindrift/issues/7502)
* **github:** require gitRemote to sit on the GH_HOST host ([fef71e3](https://github.com/jordansmall/spindrift/commit/fef71e3de0483bcaf698b3b5c8040099c177de52)), closes [#7538](https://github.com/jordansmall/spindrift/issues/7538)
* list the Route header sets and how to declare an extra ([e879bf1](https://github.com/jordansmall/spindrift/commit/e879bf1cc68ef725320e9ddee5d4d42198e09b91)), closes [#7565](https://github.com/jordansmall/spindrift/issues/7565)
* **migrating:** note the per-checkout Box name ([5261061](https://github.com/jordansmall/spindrift/commit/526106186ed5dcd85fddb69640e88b27f397fd2f)), closes [#7378](https://github.com/jordansmall/spindrift/issues/7378)
* record Connector registration as data-only ([56617e8](https://github.com/jordansmall/spindrift/commit/56617e8a970f64e37fd9d820c97cd60465ff6c71)), closes [#7562](https://github.com/jordansmall/spindrift/issues/7562)
* **reference:** describe Connector protocol 0.4.0 ([a14d137](https://github.com/jordansmall/spindrift/commit/a14d137efba995522c91dbd76c761da48a8d2498)), closes [#7510](https://github.com/jordansmall/spindrift/issues/7510)
* **reference:** describe one Connector runtime per process ([ff8142f](https://github.com/jordansmall/spindrift/commit/ff8142f5b2b84b303a6b95569bb3d7f7c43f9632)), closes [#7554](https://github.com/jordansmall/spindrift/issues/7554)
* **reference:** describe the pinned, read-only Connector sandbox ([0ba2afb](https://github.com/jordansmall/spindrift/commit/0ba2afbfe0aadc8dcec2d94bf13fd8033686b253)), closes [#7552](https://github.com/jordansmall/spindrift/issues/7552)
* **reference:** describe the row credential as read at use ([6bc7776](https://github.com/jordansmall/spindrift/commit/6bc77764cd51eba863c32e5413dc6209703e70e6)), closes [#7549](https://github.com/jordansmall/spindrift/issues/7549)
* **reference:** describe the SDK fake host as a test aid ([783328e](https://github.com/jordansmall/spindrift/commit/783328e7eb1d923ffb485e26424d33e309a300a1)), closes [#7568](https://github.com/jordansmall/spindrift/issues/7568)
* **reference:** describe the SDK's JSON, escape and page helpers ([bda0ca5](https://github.com/jordansmall/spindrift/commit/bda0ca586f42333bbc994dfb4587c094b3d62415)), closes [#7570](https://github.com/jordansmall/spindrift/issues/7570)
* **reference:** describe the third-party baseline refusal ([309dac2](https://github.com/jordansmall/spindrift/commit/309dac2203a094193d9db323bd336540a4475ab5)), closes [#7564](https://github.com/jordansmall/spindrift/issues/7564)
* **reference:** describe what the SDK fake host checks ([e110e11](https://github.com/jordansmall/spindrift/commit/e110e11c28bb6b94b9c399e1f5a6125a833df382)), closes [#7573](https://github.com/jordansmall/spindrift/issues/7573)
* **reference:** name a third-party Connector's credential variable ([aa6f507](https://github.com/jordansmall/spindrift/commit/aa6f5076e06e4cea3e0a4e44d7a3e5d8dafceb2d)), closes [#7560](https://github.com/jordansmall/spindrift/issues/7560)
* **reference:** point Connector authors at the protocol description ([a77ce67](https://github.com/jordansmall/spindrift/commit/a77ce679276cf17171c96a5caaac540a17d7a3b3)), closes [#7505](https://github.com/jordansmall/spindrift/issues/7505)
* **reference:** show the Route admit cases as the documented rule ([f70cdf2](https://github.com/jordansmall/spindrift/commit/f70cdf2320ed8c32ebb209c81b8eb5ea19bc3de9)), closes [#7569](https://github.com/jordansmall/spindrift/issues/7569)
* say a Connector registers as data in the last stale lines ([58fb5d9](https://github.com/jordansmall/spindrift/commit/58fb5d9a161ab1214c664d74ba8316a1e4bbc057)), closes [#7562](https://github.com/jordansmall/spindrift/issues/7562)


### Code Refactoring

* **backendrows:** decode backend rows into a value Set ([4174cce](https://github.com/jordansmall/spindrift/commit/4174ccee1613c2bd5b8f37379d05134c69ce1d94)), closes [#7555](https://github.com/jordansmall/spindrift/issues/7555)
* **backendrows:** delete the process-wide backend-row registry ([f85e3e4](https://github.com/jordansmall/spindrift/commit/f85e3e4e1be0ad068ae59ed0df5af891be508106)), closes [#7555](https://github.com/jordansmall/spindrift/issues/7555)
* **box:** build the Box-side Connector runtime from the row file ([dc1604a](https://github.com/jordansmall/spindrift/commit/dc1604a829028058f2587f8f25dc3ac8c98fca1d)), closes [#7556](https://github.com/jordansmall/spindrift/issues/7556)
* **checks:** drop the scaffold check's module-form handling ([6a4cb51](https://github.com/jordansmall/spindrift/commit/6a4cb51e96c7070793a44ee39070e65e6200473a)), closes [#7562](https://github.com/jordansmall/spindrift/issues/7562)
* **checks:** share the hasAll and fails helpers ([8c821e1](https://github.com/jordansmall/spindrift/commit/8c821e1dd57e66222bffa9eafa71c37b9531294d)), closes [#7562](https://github.com/jordansmall/spindrift/issues/7562)
* **connectorcall:** look result checks up through the method table ([26b110e](https://github.com/jordansmall/spindrift/commit/26b110e38ba11ae98d9856518403e28aa0eb94f2)), closes [#7506](https://github.com/jordansmall/spindrift/issues/7506)
* **connectorcall:** read retry and capability from the method table ([cb1c4e7](https://github.com/jordansmall/spindrift/commit/cb1c4e7f6bc21fd05ecc5509771677d93be0c9b0)), closes [#7506](https://github.com/jordansmall/spindrift/issues/7506)
* **connectorforge:** probe liveness without forge.remote ([84db84d](https://github.com/jordansmall/spindrift/commit/84db84ddbff486dc1cec63d691eb95ee41f3915b)), closes [#7509](https://github.com/jordansmall/spindrift/issues/7509)
* **connectorforge:** register the forge result checks by name ([d72e9a0](https://github.com/jordansmall/spindrift/commit/d72e9a0896d048da45ca185e46863fc8c2e02a6d)), closes [#7506](https://github.com/jordansmall/spindrift/issues/7506)
* **connectorhost:** drop the unused DeclaredHosts ([671fe53](https://github.com/jordansmall/spindrift/commit/671fe538b525ff8ce0a97a1a408a9b27dabf9496)), closes [#7508](https://github.com/jordansmall/spindrift/issues/7508)
* **connectorruntime:** run close hooks and reach totals per backend ([28f1e88](https://github.com/jordansmall/spindrift/commit/28f1e8817e64c5b84a24c762c1abbe5753b137bf)), closes [#7554](https://github.com/jordansmall/spindrift/issues/7554) [#7117](https://github.com/jordansmall/spindrift/issues/7117)
* **connectorruntime:** take the sandbox explicitly ([d451c4b](https://github.com/jordansmall/spindrift/commit/d451c4b53d433e9db3332c18555a1ba7c0993f2b)), closes [#7556](https://github.com/jordansmall/spindrift/issues/7556)
* **connectorsandbox:** delete the process-wide sandbox install ([6cc469a](https://github.com/jordansmall/spindrift/commit/6cc469a003f412bac852a3951170acded02d4437)), closes [#7556](https://github.com/jordansmall/spindrift/issues/7556)
* **connectors:** drop the flake-module registration path ([cc88d2e](https://github.com/jordansmall/spindrift/commit/cc88d2e506394742c94394806726e4ea351843ff)), closes [#7562](https://github.com/jordansmall/spindrift/issues/7562)
* **connectorsetup:** build the Connector runtime in one constructor ([6874358](https://github.com/jordansmall/spindrift/commit/6874358136c455f3039498ed561691902deeb0bb)), closes [#7554](https://github.com/jordansmall/spindrift/issues/7554)
* **connectortest:** share the scripted Connector helpers ([6a0bd11](https://github.com/jordansmall/spindrift/commit/6a0bd11ebd3aa6ff2d3870f3fcebb455d3e63e81)), closes [#7429](https://github.com/jordansmall/spindrift/issues/7429)
* **connectortracker:** drop own reloader ([264b32f](https://github.com/jordansmall/spindrift/commit/264b32f3ead5f9c709ae2584a3f0e5e11dd1653c)), closes [#7546](https://github.com/jordansmall/spindrift/issues/7546)
* **connectortracker:** register the tracker result checks by name ([76cdc96](https://github.com/jordansmall/spindrift/commit/76cdc963ba4d6abc3fdb564a2c3eed417cc015df)), closes [#7506](https://github.com/jordansmall/spindrift/issues/7506)
* **daemon:** build the Connector runtime through connectorsetup ([b60c1fe](https://github.com/jordansmall/spindrift/commit/b60c1feed70bd8488483c4940edc59093d0ddb8b)), closes [#7554](https://github.com/jordansmall/spindrift/issues/7554) [#7325](https://github.com/jordansmall/spindrift/issues/7325)
* **daemon:** resolve row credentials through rowcredential ([df41cf7](https://github.com/jordansmall/spindrift/commit/df41cf7a0400f27816f93157e16342cd0361a259)), closes [#7503](https://github.com/jordansmall/spindrift/issues/7503)
* **dispatchrecord:** resolve a log's Driver from a given Set ([dbe3511](https://github.com/jordansmall/spindrift/commit/dbe35111db19a2aba78afb2d98bf46e8bc1ed3e5)), closes [#7556](https://github.com/jordansmall/spindrift/issues/7556)
* **driver:** delete the process-wide Driver registry ([9c77984](https://github.com/jordansmall/spindrift/commit/9c77984ae34bb1f043f35fcb64c6c0a6746999bd)), closes [#7556](https://github.com/jordansmall/spindrift/issues/7556)
* **driver:** hold Drivers and their rows in a Set value ([7b19554](https://github.com/jordansmall/spindrift/commit/7b19554cfd3ebae1b242753809499d3c141bea77)), closes [#7556](https://github.com/jordansmall/spindrift/issues/7556)
* **driverkit:** encode the session file beside its decoder ([6d90ce9](https://github.com/jordansmall/spindrift/commit/6d90ce939bc279ec71943fea6d1aeee2703f62a6)), closes [#7507](https://github.com/jordansmall/spindrift/issues/7507)
* **forge:** drop per-backend credential checks ([fab39dc](https://github.com/jordansmall/spindrift/commit/fab39dc82200137806ad0751d29d9fb5f47baf1d)), closes [#7502](https://github.com/jordansmall/spindrift/issues/7502)
* **forge:** move TokenIntrospection out of backendkit ([1fdae99](https://github.com/jordansmall/spindrift/commit/1fdae9992f2a08299687c1580a45f94c72606fa9)), closes [#7547](https://github.com/jordansmall/spindrift/issues/7547)
* **gitexec:** let a credential read its token at each use ([da4ba30](https://github.com/jordansmall/spindrift/commit/da4ba30fd66a5826347c391a34cc4334ba90558e)), closes [#7500](https://github.com/jordansmall/spindrift/issues/7500)
* **github:** read the github row from the runtime it is handed ([7845859](https://github.com/jordansmall/spindrift/commit/7845859b00d0362b3dd43d1b3ebb4f50fc63a1de)), closes [#7555](https://github.com/jordansmall/spindrift/issues/7555)
* **github:** read the Ledger credential at each push ([bbd750f](https://github.com/jordansmall/spindrift/commit/bbd750f7813557cd03f8431d439ea10a656e5bf7)), closes [#7499](https://github.com/jordansmall/spindrift/issues/7499)
* **launcher:** build the Connector runtime once per process ([545a1a0](https://github.com/jordansmall/spindrift/commit/545a1a0f44ee2b8fc814615f9a46272d4cbd90a6)), closes [#7554](https://github.com/jordansmall/spindrift/issues/7554) [#7117](https://github.com/jordansmall/spindrift/issues/7117)
* **launcher:** decode backend rows once into the Connector runtime ([2dd0d2b](https://github.com/jordansmall/spindrift/commit/2dd0d2b5e5a086c525084a4b9fa9f68b8d13b3c7)), closes [#7555](https://github.com/jordansmall/spindrift/issues/7555)
* **launcher:** delete hostTokenValue ([6a31f2f](https://github.com/jordansmall/spindrift/commit/6a31f2f9d11ff55173f7d52b1bb75fc255344b4c)), closes [#7502](https://github.com/jordansmall/spindrift/issues/7502)
* **launcher:** read forgejo, doctor, write-gate rows from runtime ([7e47c06](https://github.com/jordansmall/spindrift/commit/7e47c060f9132236e1a4be0e86a750024a7b0450)), closes [#7555](https://github.com/jordansmall/spindrift/issues/7555)
* **launcher:** resolve row credentials through rowcredential ([7f8c4ba](https://github.com/jordansmall/spindrift/commit/7f8c4ba7dd5536bc1b596688b91a3d97791e9f90)), closes [#7503](https://github.com/jordansmall/spindrift/issues/7503)
* **launcher:** stop writing row credentials into the environment ([ec43f9d](https://github.com/jordansmall/spindrift/commit/ec43f9dc2aaa6964413c707c8c4c216920e79fd0)), closes [#7549](https://github.com/jordansmall/spindrift/issues/7549)
* **launcher:** take Drivers from the program's Connector runtime ([7aece7d](https://github.com/jordansmall/spindrift/commit/7aece7d44148b6c03f37e28b68dba07de25cf324)), closes [#7556](https://github.com/jordansmall/spindrift/issues/7556)
* **runner:** resolve and build a runner family in one call ([a90d039](https://github.com/jordansmall/spindrift/commit/a90d039b4ee1588b6d71d67d75cf6626cf3dd5c5)), closes [#7554](https://github.com/jordansmall/spindrift/issues/7554) [#7325](https://github.com/jordansmall/spindrift/issues/7325)
* **sdk:** key tracker handlers by method name ([510c2dc](https://github.com/jordansmall/spindrift/commit/510c2dc05c7a2b0cfcd1d2c9b7ba1604c4b9627a)), closes [#7505](https://github.com/jordansmall/spindrift/issues/7505)
* **tokenrefresh:** share the GH_TOKEN watcher and drop Follow ([9c9b642](https://github.com/jordansmall/spindrift/commit/9c9b6428052255e681731a47ec95a4ce6722061e)), closes [#7499](https://github.com/jordansmall/spindrift/issues/7499)


### Tests

* **bwrap:** pin Kill across two checkouts' Boxes ([e1333a9](https://github.com/jordansmall/spindrift/commit/e1333a98962efa3e49fc60a7b8938080f8ca81cb)), closes [#7378](https://github.com/jordansmall/spindrift/issues/7378)
* **checks:** refuse a firstParty claim made from Connector data ([e0b92f0](https://github.com/jordansmall/spindrift/commit/e0b92f0c31401db39580c9268bdf43d243ecdab4)), closes [#7559](https://github.com/jordansmall/spindrift/issues/7559)
* **checks:** run fixture-free core go tests without the fixture build ([2c7fd75](https://github.com/jordansmall/spindrift/commit/2c7fd75042532fb9d4f924b148a4cc75238f664d)), closes [#7577](https://github.com/jordansmall/spindrift/issues/7577)
* **ci:** cover the check partitioner with a fixture drv graph ([9bd703d](https://github.com/jordansmall/spindrift/commit/9bd703d3f66a89375156fc0c3897f7e2bc1923ec)), closes [#7523](https://github.com/jordansmall/spindrift/issues/7523)
* **connector-protocol:** add header and document admit cases ([c1ff86d](https://github.com/jordansmall/spindrift/commit/c1ff86d870b999728410ea86e82c3abf375f05c1)), closes [#7573](https://github.com/jordansmall/spindrift/issues/7573)
* **connector-protocol:** run the admit cases against both hosts ([2015164](https://github.com/jordansmall/spindrift/commit/20151641ffb75c0ff0350b862abcdc08413d8c3f)), closes [#7569](https://github.com/jordansmall/spindrift/issues/7569)
* **connector-registration:** refuse a row override under batteries ([569ca23](https://github.com/jordansmall/spindrift/commit/569ca23e95a62040b7a582d44461f2bd7e29cab2)), closes [#7562](https://github.com/jordansmall/spindrift/issues/7562)
* **connector-registration:** tie the checks to what core saw ([b4fb664](https://github.com/jordansmall/spindrift/commit/b4fb66490ece17350e0c8ecba6944bc8e47d3f9f)), closes [#7562](https://github.com/jordansmall/spindrift/issues/7562)
* **connectorforge:** point git at fixtures through a shared config ([2630168](https://github.com/jordansmall/spindrift/commit/263016892cd909b4c98aeb8dc89f4b565f9cd537)), closes [#7575](https://github.com/jordansmall/spindrift/issues/7575)
* **connectorforge:** rotate the token mid-run in the composed forge ([8efd834](https://github.com/jordansmall/spindrift/commit/8efd834ba91d4b8566c71e545e9a8650c09b3c27)), closes [#7500](https://github.com/jordansmall/spindrift/issues/7500)
* **connectorforge:** run tests that touch no global state in parallel ([8506d1b](https://github.com/jordansmall/spindrift/commit/8506d1ba394ea9cce2ca4a7e82f2648c995b972e)), closes [#7575](https://github.com/jordansmall/spindrift/issues/7575)
* **connectorhost:** admit the loopback test backend through a test seam ([801cd11](https://github.com/jordansmall/spindrift/commit/801cd1168c93d7e5764cd54470325e16c4fc5a61)), closes [#7567](https://github.com/jordansmall/spindrift/issues/7567)
* **connectorhost:** feed the in-goroutine remote host an OS pipe ([cb22c98](https://github.com/jordansmall/spindrift/commit/cb22c98dcbb089893f7e1654f4cf4f32bdae6ccc)), closes [#7575](https://github.com/jordansmall/spindrift/issues/7575)
* **connectorhost:** load FuzzLoad's seed corpus in parallel ([73480ec](https://github.com/jordansmall/spindrift/commit/73480ec0c332e7b18eb9ca90bf1241b0aa2a9964)), closes [#7575](https://github.com/jordansmall/spindrift/issues/7575)
* **connectorhost:** overlap the hostile tests' budget-exhausting cases ([eed2e29](https://github.com/jordansmall/spindrift/commit/eed2e29ce3aba7852b0272d18a47a8fbdf2d1c67)), closes [#7575](https://github.com/jordansmall/spindrift/issues/7575)
* **connectorhost:** overlap the two slow-backend call-budget tests ([74cb74f](https://github.com/jordansmall/spindrift/commit/74cb74f5e86b2e7b99423ffbc1cd85a45afd7705)), closes [#7575](https://github.com/jordansmall/spindrift/issues/7575)
* **connectorhost:** run tests that touch no global state in parallel ([5444808](https://github.com/jordansmall/spindrift/commit/544480876889eb70217bb1239f50ae2cea1b676a)), closes [#7575](https://github.com/jordansmall/spindrift/issues/7575)
* **connectorhost:** run the hostile fixtures' production cases serially ([e7ee25f](https://github.com/jordansmall/spindrift/commit/e7ee25f4da07efd1add4ad55ba71e0d4e1d16b46)), closes [#7561](https://github.com/jordansmall/spindrift/issues/7561)
* **connectorhost:** run the one-shot test's batch decode once ([fc5acdf](https://github.com/jordansmall/spindrift/commit/fc5acdfbe253cbf1a1caacce60d7a55ebff7b605)), closes [#7575](https://github.com/jordansmall/spindrift/issues/7575)
* **connectorhost:** share compiled fixture modules across a test run ([b02c56d](https://github.com/jordansmall/spindrift/commit/b02c56df64c8369a7dfd4b36683cf9d4b2be9555)), closes [#7575](https://github.com/jordansmall/spindrift/issues/7575)
* **connectorprotocol:** validate host-test exchanges against the schema ([3390fe2](https://github.com/jordansmall/spindrift/commit/3390fe2b2559789153fc3ef417da1bacbfa798b3)), closes [#7504](https://github.com/jordansmall/spindrift/issues/7504)
* **connectortracker:** move signal test out ([10c353a](https://github.com/jordansmall/spindrift/commit/10c353ac884d27b1662173c804dcd961b19ff0e5)), closes [#7546](https://github.com/jordansmall/spindrift/issues/7546)
* **escape:** forge a frame under another Connector's session ([fc96aa5](https://github.com/jordansmall/spindrift/commit/fc96aa55eae5f7645ef48939d6706fec894ad2f9)), closes [#7550](https://github.com/jordansmall/spindrift/issues/7550)
* **escape:** try to write under the sandbox root ([5f94cb5](https://github.com/jordansmall/spindrift/commit/5f94cb53860ba972e20d620f9fb7ffbba2ba7a4f)), closes [#7552](https://github.com/jordansmall/spindrift/issues/7552)
* **forgejo:** cover a self-hosted instance under a subpath ([134dfd9](https://github.com/jordansmall/spindrift/commit/134dfd9a8ae5d4faf624e627be143f1844c8969f)), closes [#7509](https://github.com/jordansmall/spindrift/issues/7509)
* **fragment-paths:** cover forgejo fragments registered as data ([c15e138](https://github.com/jordansmall/spindrift/commit/c15e13823119736a5e32feafdf94cdefe61f5ec8)), closes [#7293](https://github.com/jordansmall/spindrift/issues/7293) [#7562](https://github.com/jordansmall/spindrift/issues/7562)
* **github:** pin one session per row and reload after a guest kill ([2c69784](https://github.com/jordansmall/spindrift/commit/2c69784a4aba6c6f7e5a21ed9d51054c102af2d0)), closes [#7102](https://github.com/jordansmall/spindrift/issues/7102)
* **github:** prove a refreshed GH_TOKEN reaches the github row ([4a3e45d](https://github.com/jordansmall/spindrift/commit/4a3e45d2faac47f3ae304fbd85e4790f75771cc9)), closes [#7499](https://github.com/jordansmall/spindrift/issues/7499)
* **labels:** pin the mirrored label literals to the env schema ([0b58bd9](https://github.com/jordansmall/spindrift/commit/0b58bd9bdbcbdb98cacf7ff25735afbad055296d)), closes [#7429](https://github.com/jordansmall/spindrift/issues/7429)
* **launcher:** give each backend-row test its own row set ([922396e](https://github.com/jordansmall/spindrift/commit/922396eed751fe4c6750ab6e431fed0f6af1421f)), closes [#7555](https://github.com/jordansmall/spindrift/issues/7555)
* **nix:** list the protocol's contract paths instead of scraping them ([8712e44](https://github.com/jordansmall/spindrift/commit/8712e44362a717723629ced77363660fa1013399)), closes [#7505](https://github.com/jordansmall/spindrift/issues/7505)
* **nix:** pin which images build compressed ([c246269](https://github.com/jordansmall/spindrift/commit/c246269076ff7f617204971681b1ea6b582b480f)), closes [#7522](https://github.com/jordansmall/spindrift/issues/7522)
* **orchestrator:** write session fixtures in the JSON-array shape ([faf31d9](https://github.com/jordansmall/spindrift/commit/faf31d95645968adeefcee2c209eb5c739f969a0)), closes [#7507](https://github.com/jordansmall/spindrift/issues/7507)
* **runner:** stop bounding the sandbox contract's load ([4eb2f89](https://github.com/jordansmall/spindrift/commit/4eb2f8962349c3c9a7243a7e34af0c5f1bffe83e)), closes [#7559](https://github.com/jordansmall/spindrift/issues/7559)
* **sdk:** declare X-Token on the SendJSON test's POST route ([b8ebfab](https://github.com/jordansmall/spindrift/commit/b8ebfab838ad3b6b8e59ce481c55454a9fccb562))
* **sdk:** drop TestAdmit rows the shared admit cases now cover ([7b2c390](https://github.com/jordansmall/spindrift/commit/7b2c3900756ef82dbc0727d79ca1786f8f0d312e)), closes [#7569](https://github.com/jordansmall/spindrift/issues/7569)
* **sdk:** drop the parity tests that read core's source ([6995d8e](https://github.com/jordansmall/spindrift/commit/6995d8e01d08e189d4287e051ab92769943e85da)), closes [#7505](https://github.com/jordansmall/spindrift/issues/7505)
* **seamtest:** bench two and three Connector sandboxes per run ([4486071](https://github.com/jordansmall/spindrift/commit/4486071ba5ebdc555aec9a94b6dddabcead00eba)), closes [#7553](https://github.com/jordansmall/spindrift/issues/7553)


### Build System

* **connectors:** export the Route table to a backend Connector's tests ([eae72e1](https://github.com/jordansmall/spindrift/commit/eae72e1a1096f4d8a5a0d4081672ee4e14ed5e84)), closes [#7568](https://github.com/jordansmall/spindrift/issues/7568)


### Continuous Integration

* add a script that partitions checks by image closure ([6eecb63](https://github.com/jordansmall/spindrift/commit/6eecb63442ad5520b8258a937fabf402e298578c)), closes [#7523](https://github.com/jordansmall/spindrift/issues/7523)
* build each Linux leg's image checks in a parallel job ([70b7242](https://github.com/jordansmall/spindrift/commit/70b724293ee9b568721a9e9555de8f8ae909740a)), closes [#7524](https://github.com/jordansmall/spindrift/issues/7524)


### Miscellaneous Chores

* **connectors/github:** drop the committed build binary ([107eb25](https://github.com/jordansmall/spindrift/commit/107eb2521026634d901a19cb7ea8746e868c9cf2)), closes [#7510](https://github.com/jordansmall/spindrift/issues/7510)
* regenerate the Route baseline with connectors accept ([54d2b67](https://github.com/jordansmall/spindrift/commit/54d2b675d0e990eddf37e7d5dc0e3cefcac9dece)), closes [#7154](https://github.com/jordansmall/spindrift/issues/7154) [#7563](https://github.com/jordansmall/spindrift/issues/7563)
</details>

<details><summary>claude: 0.4.0</summary>

## [0.4.0](https://github.com/jordansmall/spindrift/compare/claude-v0.3.1...claude-v0.4.0) (2026-11-28)


###   BREAKING CHANGES

* **connectors:** Connector flakes no longer export flakeModules.default and core.lib.connectorModule is gone; register a Connector by adding its connectorData output to spindrift.connectorData.
* **driver:** the SDK's driver.Strategy.SessionFlags returns a driver.SessionFlag instead of a string, and the driver.sessionFlags result is {flag, value} instead of {flags}.

### Features

* **connectors:** register the claude, opencode and pi Drivers as data ([64601ef](https://github.com/jordansmall/spindrift/commit/64601efe156d381cd20b44fc7c1ab4f7a70e4a86)), closes [#7559](https://github.com/jordansmall/spindrift/issues/7559)
* **driver:** answer driver.sessionFlags as a flag and a value ([f475053](https://github.com/jordansmall/spindrift/commit/f4750538566e44c9c1eb34b0b823771b2a9e11a8)), closes [#7507](https://github.com/jordansmall/spindrift/issues/7507)


### Code Refactoring

* **connectors:** drop the flake-module registration path ([cc88d2e](https://github.com/jordansmall/spindrift/commit/cc88d2e506394742c94394806726e4ea351843ff)), closes [#7562](https://github.com/jordansmall/spindrift/issues/7562)
</details>

<details><summary>forgejo: 0.4.0</summary>

## [0.4.0](https://github.com/jordansmall/spindrift/compare/forgejo-v0.3.0...forgejo-v0.4.0) (2026-11-28)


###   BREAKING CHANGES

* **connectors:** Connector flakes no longer export flakeModules.default and core.lib.connectorModule is gone; register a Connector by adding its connectorData output to spindrift.connectorData.
* **connector/forgejo:** a forgejo listing of more than 100 pages now fails with a permanent error naming the bound, instead of paging on until the host closes the Connector.
* **protocol:** core refuses a forge or Driver Connector built against protocol 0.3; rebuild it against the 0.4 SDK.
* **forge:** sdk/forge's Probe moved from PRs to Branches and is required there. A forge Connector without forge.probe no longer composes.
* **protocol:** forge.remote is no longer part of the protocol and sdk/forge's Branches has no Remote field. An out-of-tree forge Connector must drop it.
* **forgejo:** SPINDRIFT_FORGEJO_CONNECTOR_TOKEN and SPINDRIFT_FORGEJO_CONNECTOR_TOKEN_CMD are removed. Set FORGEJO_TOKEN (or FORGEJO_TOKEN_CMD), which now authorizes forgejo Connector requests and git.

### Features

* **connectors:** declare the headers github and forgejo send and read ([d1ef6e6](https://github.com/jordansmall/spindrift/commit/d1ef6e637e7f2c3109a2f1c8c9a23d75006ca3b7)), closes [#7565](https://github.com/jordansmall/spindrift/issues/7565)
* **connectors:** register the forgejo Connector as data ([1ecdee7](https://github.com/jordansmall/spindrift/commit/1ecdee7a0575f1db095bf62ac13781b1f77326cb)), closes [#7558](https://github.com/jordansmall/spindrift/issues/7558)
* **forgejo:** authorize the forgejo Connector with FORGEJO_TOKEN ([a47ee04](https://github.com/jordansmall/spindrift/commit/a47ee04d344317efc4e8d536acdf9ad3b3aa2e47)), closes [#7500](https://github.com/jordansmall/spindrift/issues/7500)
* **forge:** probe a forge's liveness through forge.probe ([1c588aa](https://github.com/jordansmall/spindrift/commit/1c588aa53cbed60d6a2cb3e9d371c95bdda115ce)), closes [#7510](https://github.com/jordansmall/spindrift/issues/7510)
* **nix:** declare each forge Connector's Git remote on its row ([1ac9d03](https://github.com/jordansmall/spindrift/commit/1ac9d03953f885f10f497c545a9ebe9f11ae52b8)), closes [#7508](https://github.com/jordansmall/spindrift/issues/7508)
* **protocol:** remove forge.remote from the Connector protocol ([5f48d7a](https://github.com/jordansmall/spindrift/commit/5f48d7a9d72f646410fde99d56fc343c97868bb0)), closes [#7510](https://github.com/jordansmall/spindrift/issues/7510)
* **protocol:** speak Connector protocol 0.4.0 ([217fa22](https://github.com/jordansmall/spindrift/commit/217fa22ba34d238b87a1a79e491f42960c0478d7)), closes [#7510](https://github.com/jordansmall/spindrift/issues/7510)


### Bug Fixes

* **connector/forgejo:** class a failed send transient, via SDK helpers ([3df3b72](https://github.com/jordansmall/spindrift/commit/3df3b722907f03ce500067cb590787e9de052c8a)), closes [#7570](https://github.com/jordansmall/spindrift/issues/7570)
* **connector/forgejo:** fail a listing past 100 pages, not page on ([30065aa](https://github.com/jordansmall/spindrift/commit/30065aad062dc9b22f1e254b98dec81d16aaa206)), closes [#7570](https://github.com/jordansmall/spindrift/issues/7570)
* **connector/forgejo:** re-check every issue before trusting the count ([5d802c3](https://github.com/jordansmall/spindrift/commit/5d802c3ae51ef1bec48014d0ec6f684f83c57976)), closes [#7409](https://github.com/jordansmall/spindrift/issues/7409) [#7570](https://github.com/jordansmall/spindrift/issues/7570)
* **connectors/forgejo:** refuse a pull request in tracker issue() ([e964a9f](https://github.com/jordansmall/spindrift/commit/e964a9fad9ac17ef39d14a43fe933a3e80e2c718)), closes [#7358](https://github.com/jordansmall/spindrift/issues/7358)


### Security

* **connectors:** narrow the job-log Routes to a job's log ([62821a4](https://github.com/jordansmall/spindrift/commit/62821a458aa9918493da52eaf641312a805f7995)), closes [#6926](https://github.com/jordansmall/spindrift/issues/6926)


### Code Refactoring

* **connector/forgejo:** drop the send seam, test on the fake host ([50bfe67](https://github.com/jordansmall/spindrift/commit/50bfe674ee9658f64c1a3c44c205ffce03cbac24)), closes [#7570](https://github.com/jordansmall/spindrift/issues/7570)
* **connectorforge:** probe liveness without forge.remote ([84db84d](https://github.com/jordansmall/spindrift/commit/84db84ddbff486dc1cec63d691eb95ee41f3915b)), closes [#7509](https://github.com/jordansmall/spindrift/issues/7509)
* **connectors:** drop the flake-module registration path ([cc88d2e](https://github.com/jordansmall/spindrift/commit/cc88d2e506394742c94394806726e4ea351843ff)), closes [#7562](https://github.com/jordansmall/spindrift/issues/7562)


### Tests

* **forgejo:** cover a self-hosted instance under a subpath ([134dfd9](https://github.com/jordansmall/spindrift/commit/134dfd9a8ae5d4faf624e627be143f1844c8969f)), closes [#7509](https://github.com/jordansmall/spindrift/issues/7509)
* **forgejo:** run the hostile corpus and the capability check ([bfcf036](https://github.com/jordansmall/spindrift/commit/bfcf0363f8dc56a15e6e50b38ea6100d73d6edfa)), closes [#7574](https://github.com/jordansmall/spindrift/issues/7574)
</details>

<details><summary>github: 0.6.0</summary>

## [0.6.0](https://github.com/jordansmall/spindrift/compare/github-v0.5.0...github-v0.6.0) (2026-11-28)


###   BREAKING CHANGES

* **connectors:** Connector flakes no longer export flakeModules.default and core.lib.connectorModule is gone; register a Connector by adding its connectorData output to spindrift.connectorData.
* **protocol:** core refuses a forge or Driver Connector built against protocol 0.3; rebuild it against the 0.4 SDK.
* **forge:** sdk/forge's Probe moved from PRs to Branches and is required there. A forge Connector without forge.probe no longer composes.
* **protocol:** forge.remote is no longer part of the protocol and sdk/forge's Branches has no Remote field. An out-of-tree forge Connector must drop it.
* **github:** SPINDRIFT_GITHUB_CONNECTOR_TOKEN is no longer read; set GH_TOKEN instead.

### Features

* **connector/github:** send only fixed GraphQL documents and declare them ([4d4e108](https://github.com/jordansmall/spindrift/commit/4d4e108f5ab96bc35ae7988406c02301df3c99cc)), closes [#7566](https://github.com/jordansmall/spindrift/issues/7566)
* **connectors:** declare the headers github and forgejo send and read ([d1ef6e6](https://github.com/jordansmall/spindrift/commit/d1ef6e637e7f2c3109a2f1c8c9a23d75006ca3b7)), closes [#7565](https://github.com/jordansmall/spindrift/issues/7565)
* **connectors:** register the fixture and github Connectors as data ([5d86d44](https://github.com/jordansmall/spindrift/commit/5d86d4433d509c6e150a24ac6add834ff6c38427)), closes [#7557](https://github.com/jordansmall/spindrift/issues/7557)
* **forge:** probe a forge's liveness through forge.probe ([1c588aa](https://github.com/jordansmall/spindrift/commit/1c588aa53cbed60d6a2cb3e9d371c95bdda115ce)), closes [#7510](https://github.com/jordansmall/spindrift/issues/7510)
* **github:** name GH_TOKEN as the github Connector's credential ([5008018](https://github.com/jordansmall/spindrift/commit/5008018db4540c7bc5fa62bf46ef089018a485e5)), closes [#7499](https://github.com/jordansmall/spindrift/issues/7499)
* **nix:** declare each forge Connector's Git remote on its row ([1ac9d03](https://github.com/jordansmall/spindrift/commit/1ac9d03953f885f10f497c545a9ebe9f11ae52b8)), closes [#7508](https://github.com/jordansmall/spindrift/issues/7508)
* **protocol:** remove forge.remote from the Connector protocol ([5f48d7a](https://github.com/jordansmall/spindrift/commit/5f48d7a9d72f646410fde99d56fc343c97868bb0)), closes [#7510](https://github.com/jordansmall/spindrift/issues/7510)
* **protocol:** speak Connector protocol 0.4.0 ([217fa22](https://github.com/jordansmall/spindrift/commit/217fa22ba34d238b87a1a79e491f42960c0478d7)), closes [#7510](https://github.com/jordansmall/spindrift/issues/7510)


### Bug Fixes

* **connector/github:** class a failed send transient, via SendJSON ([921433b](https://github.com/jordansmall/spindrift/commit/921433b1630f706f6abe085d2899f4d6717c9d7f)), closes [#7572](https://github.com/jordansmall/spindrift/issues/7572)
* **connector/github:** escape branch segments with PathSegment ([e46eb78](https://github.com/jordansmall/spindrift/commit/e46eb78422a5af4c1756b53efd3f98d23ec733f2)), closes [#7572](https://github.com/jordansmall/spindrift/issues/7572)


### Security

* **connectorhost:** address-check the redirect hop, audit its 3xx ([6575cdd](https://github.com/jordansmall/spindrift/commit/6575cdd3fe9f18b286609b0d74dff6e171009005)), closes [#7567](https://github.com/jordansmall/spindrift/issues/7567) [#6926](https://github.com/jordansmall/spindrift/issues/6926)
* **connectors:** narrow the job-log Routes to a job's log ([62821a4](https://github.com/jordansmall/spindrift/commit/62821a458aa9918493da52eaf641312a805f7995)), closes [#6926](https://github.com/jordansmall/spindrift/issues/6926)


### Code Refactoring

* **connector/github:** drop the send seam, test on the fake host ([b1ab788](https://github.com/jordansmall/spindrift/commit/b1ab7887a0742f42fbe2daaf627a3af5ce0c90ac)), closes [#7572](https://github.com/jordansmall/spindrift/issues/7572)
* **connector/github:** page REST listings through connector.Pages ([31696c1](https://github.com/jordansmall/spindrift/commit/31696c15ed5ed1010e807b25f75e6aa2bd6224c8)), closes [#7572](https://github.com/jordansmall/spindrift/issues/7572)
* **connectors:** drop the flake-module registration path ([cc88d2e](https://github.com/jordansmall/spindrift/commit/cc88d2e506394742c94394806726e4ea351843ff)), closes [#7562](https://github.com/jordansmall/spindrift/issues/7562)


### Tests

* **connector/github:** run native tests through the SDK fake host ([ed78eae](https://github.com/jordansmall/spindrift/commit/ed78eaeedee12096087870639182eecc963a0c24)), closes [#7573](https://github.com/jordansmall/spindrift/issues/7573)
* **github:** run the hostile corpus and the capability check ([3db1836](https://github.com/jordansmall/spindrift/commit/3db1836514eaaa50d351f39b2176c92adb017b20)), closes [#7574](https://github.com/jordansmall/spindrift/issues/7574)


### Miscellaneous Chores

* **connectors/github:** drop the committed build binary ([107eb25](https://github.com/jordansmall/spindrift/commit/107eb2521026634d901a19cb7ea8746e868c9cf2)), closes [#7510](https://github.com/jordansmall/spindrift/issues/7510)
</details>

<details><summary>jira: 0.5.0</summary>

## [0.5.0](https://github.com/jordansmall/spindrift/compare/jira-v0.4.0...jira-v0.5.0) (2026-11-28)


###   BREAKING CHANGES

* **connectors:** Connector flakes no longer export flakeModules.default and core.lib.connectorModule is gone; register a Connector by adding its connectorData output to spindrift.connectorData.
* **jira:** SPINDRIFT_JIRA_CONNECTOR_TOKEN and SPINDRIFT_JIRA_CONNECTOR_EMAIL are removed. Move their values to JIRA_TOKEN and JIRA_EMAIL, and SPINDRIFT_JIRA_CONNECTOR_TOKEN_CMD to JIRA_TOKEN_CMD.

### Features

* **connectors:** register the jira Connector as data ([e0ae5a5](https://github.com/jordansmall/spindrift/commit/e0ae5a55bfcba955aa38e1ab366acb272f21656e)), closes [#7558](https://github.com/jordansmall/spindrift/issues/7558)
* **jira:** authorize the jira Connector with JIRA_TOKEN and JIRA_EMAIL ([2cf3e6e](https://github.com/jordansmall/spindrift/commit/2cf3e6e862df2ef4626beb193e164f6eb6e9b1f3)), closes [#7501](https://github.com/jordansmall/spindrift/issues/7501)


### Bug Fixes

* **connector/jira:** page listings through the SDK's Pages loop ([9880b02](https://github.com/jordansmall/spindrift/commit/9880b026eb2558d816b3f5fd6b1a5071d45ccfa1)), closes [#7571](https://github.com/jordansmall/spindrift/issues/7571)
* **connector/jira:** send and decode through the SDK's SendJSON ([cba9c0b](https://github.com/jordansmall/spindrift/commit/cba9c0b99fe937b0dbf643cb03576b7c5003a89c)), closes [#7571](https://github.com/jordansmall/spindrift/issues/7571)


### Security

* **connector/jira:** refuse an unusable path segment before sending ([1fe6318](https://github.com/jordansmall/spindrift/commit/1fe63185bb7d1db70578773aa4bef7efc3877ef8)), closes [#7571](https://github.com/jordansmall/spindrift/issues/7571)


### Code Refactoring

* **connectors:** drop the flake-module registration path ([cc88d2e](https://github.com/jordansmall/spindrift/commit/cc88d2e506394742c94394806726e4ea351843ff)), closes [#7562](https://github.com/jordansmall/spindrift/issues/7562)
* **jira:** drop the send seam and test on the SDK fake host ([e5400f3](https://github.com/jordansmall/spindrift/commit/e5400f3b52297c3f2d003d83473313a25960b854)), closes [#7568](https://github.com/jordansmall/spindrift/issues/7568)


### Tests

* **jira:** run the hostile corpus and the capability check ([c44b6b9](https://github.com/jordansmall/spindrift/commit/c44b6b9f50920c2ad4d93413b112bdf6640b6ab2)), closes [#7574](https://github.com/jordansmall/spindrift/issues/7574)
</details>

<details><summary>opencode: 0.2.0</summary>

## [0.2.0](https://github.com/jordansmall/spindrift/compare/opencode-v0.1.2...opencode-v0.2.0) (2026-11-28)


###   BREAKING CHANGES

* **connectors:** Connector flakes no longer export flakeModules.default and core.lib.connectorModule is gone; register a Connector by adding its connectorData output to spindrift.connectorData.
* **driver:** the SDK's driver.Strategy.SessionFlags returns a driver.SessionFlag instead of a string, and the driver.sessionFlags result is {flag, value} instead of {flags}.

### Features

* **connectors:** register the claude, opencode and pi Drivers as data ([64601ef](https://github.com/jordansmall/spindrift/commit/64601efe156d381cd20b44fc7c1ab4f7a70e4a86)), closes [#7559](https://github.com/jordansmall/spindrift/issues/7559)
* **driver:** answer driver.sessionFlags as a flag and a value ([f475053](https://github.com/jordansmall/spindrift/commit/f4750538566e44c9c1eb34b0b823771b2a9e11a8)), closes [#7507](https://github.com/jordansmall/spindrift/issues/7507)


### Code Refactoring

* **connectors:** drop the flake-module registration path ([cc88d2e](https://github.com/jordansmall/spindrift/commit/cc88d2e506394742c94394806726e4ea351843ff)), closes [#7562](https://github.com/jordansmall/spindrift/issues/7562)
</details>

<details><summary>pi: 0.2.0</summary>

## [0.2.0](https://github.com/jordansmall/spindrift/compare/pi-v0.1.1...pi-v0.2.0) (2026-11-28)


###   BREAKING CHANGES

* **connectors:** Connector flakes no longer export flakeModules.default and core.lib.connectorModule is gone; register a Connector by adding its connectorData output to spindrift.connectorData.
* **driver:** the SDK's driver.Strategy.SessionFlags returns a driver.SessionFlag instead of a string, and the driver.sessionFlags result is {flag, value} instead of {flags}.

### Features

* **connectors:** register the claude, opencode and pi Drivers as data ([64601ef](https://github.com/jordansmall/spindrift/commit/64601efe156d381cd20b44fc7c1ab4f7a70e4a86)), closes [#7559](https://github.com/jordansmall/spindrift/issues/7559)
* **driver:** answer driver.sessionFlags as a flag and a value ([f475053](https://github.com/jordansmall/spindrift/commit/f4750538566e44c9c1eb34b0b823771b2a9e11a8)), closes [#7507](https://github.com/jordansmall/spindrift/issues/7507)


### Code Refactoring

* **connectors:** drop the flake-module registration path ([cc88d2e](https://github.com/jordansmall/spindrift/commit/cc88d2e506394742c94394806726e4ea351843ff)), closes [#7562](https://github.com/jordansmall/spindrift/issues/7562)
</details>

---
This PR was generated with [Release Please](https://github.com/googleapis/release-please). See [documentation](https://github.com/googleapis/release-please#release-please).