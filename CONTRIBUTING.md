## Contributing Guidelines

FMA is currently developed by a small team in a focused development spike. We welcome contributions that align with the project's goals. The FMA project accepts contributions via GitHub pull requests.

## How You Can Contribute

There are several ways you can contribute to FMA:

* **Reporting Issues:** Help us identify and fix bugs by reporting them clearly and concisely.
* **Suggesting Features:** Share your ideas for new features or improvements.
* **Improving Documentation:** Help make the project more accessible by enhancing the documentation.
* **Submitting Code Contributions (with consideration):** While the project leads maintain final say, code contributions that align with the project's vision are always welcome.

## Code of Conduct

This project adheres to the llm-d [Code of Conduct and Covenant](CODE_OF_CONDUCT.md). By participating, you are expected to uphold this code.

## Community and Communication

* **Developer Slack:** [Join our developer Slack workspace](https://llm-d.ai/slack) and participate in the **#fast-model-actuation** channel to connect with the core maintainers and other contributors, ask questions, and participate in discussions.
* **Weekly Meetings:** FMA project updates, ongoing work discussions, and Q&A are covered in our weekly project meeting every **Tuesday at 8:00 PM ET**. Join at [meet.google.com/nha-rgkw-qkw](https://meet.google.com/nha-rgkw-qkw).
* **Code**: Hosted in the [llm-d-incubation](https://github.com/llm-d-incubation) GitHub organization
* **Issues**: FMA-specific bugs or issues should be reported in [llm-d-incubation/llm-d-fast-model-actuation](https://github.com/llm-d-incubation/llm-d-fast-model-actuation/issues)
* **Mailing List**: [llm-d-contributors@googlegroups.com](mailto:llm-d-contributors@googlegroups.com) for document sharing and collaboration
* **Social Media:** Follow the main llm-d project on social media for the latest news, announcements, and updates:
  * **X:** [https://x.com/\_llm_d\_](https://x.com/_llm_d_)
  * **LinkedIn:** [https://linkedin.com/company/llm-d](https://linkedin.com/company/llm-d)
  * **Reddit:** [https://www.reddit.com/r/llm_d/](https://www.reddit.com/r/llm_d/)
  * **YouTube** [@llm-d-project](https://youtube.com/@llm-d-project)

## Contributing Process

We are a small team with defined responsibilities. All proposals must be reviewed by at least one relevant human reviewer, with broader review expected for changes with particularly wide impact.

### Types of Contributions

#### 1. Features with Public APIs or New Components

All features involving public APIs, behavior between core components, or new core repositories/subsystems should be discussed with maintainers before implementation.

**Process:**

1. Create an issue in the [FMA repository](https://github.com/llm-d-incubation/llm-d-fast-model-actuation/issues) describing:
   * **Summary**: A clear description of the change proposed and the outcome
   * **Motivation**: Problem to be solved, including Goals/Non-Goals, and any necessary background
   * **Proposal**: User stories and enough detail that reviewers can understand what you're proposing
   * **Design Details**: Specifics of your change including API specs or code snippets if applicable
   * **Alternatives**: Alternative implementations considered and why they were rejected
2. Discuss in the **#fast-model-actuation** Slack channel or weekly meeting
3. Get review from impacted component maintainers
4. Get approval from project maintainers before starting implementation

#### 2. Fixes, Issues, and Bugs

For changes that fix broken code or add small changes within a component:

* All bugs and commits must have a clear description of the bug, how to reproduce, and how the change is made
* Create an issue in the [FMA repository](https://github.com/llm-d-incubation/llm-d-fast-model-actuation/issues) or submit a pull request directly for small fixes
* A maintainer must approve the change (within the spirit of the component design and scope of change)
* For moderate size changes, create an RFC issue in GitHub and engage in the **#fast-model-actuation** Slack channel

## Feature Testing

The current testing documentation can be found within the respective components of the [docs folder](docs/).

## Code Review Requirements

* **All code changes** must be submitted as pull requests (no direct pushes)
* **All changes** must be reviewed and approved by a maintainer other than the author
* **All repositories** must gate merges on compilation and passing tests

## Commit and Pull Request Style

* **Pull requests** should describe the problem succinctly
* **Prefer smaller PRs** over larger ones; when a PR adds multiple commits, prefer smaller commits
* **Commit messages** should have:
  * Short, descriptive titles
  * Description of why the change was needed
  * Enough detail for someone reviewing git history to understand the scope
* **DCO Sign-off**: All commits must include a valid DCO sign-off line (`Signed-off-by: Name <email@domain.com>`)
  * Add automatically with `git commit -s`
  * See [PR_SIGNOFF.md](PR_SIGNOFF.md) for configuration details
  * Required for all contributions per [Developer Certificate of Origin](https://developercertificate.org/)

## API Changes and Deprecation

* **Includes**: All protocols, API endpoints, internal APIs, command line flags/arguments, and Kubernetes API object type (resource) definitions
* **Versioning**: We use [Semantic Versioning](https://semver.org) at major version 0 for Go modules and Python packages, which grants freedom to make breaking changes. For Kubernetes API object types we use the Kubernetes versioning structure and evolution rules (currently at `v1alpha1`). Since the project has no installed base, we currently make changes without regard to backward compatibility.
* **Documentation**: All APIs must have documented specs describing expected behavior

## Testing Requirements

We use two tiers of testing:

1. **Behavioral unit tests**: Fast verification of individual units of code, testing different arguments
   * Best for fast verification of parts of code, testing different arguments
   * Does not cover interaction between units of code
2. **End-to-end (e2e) tests**: Whole system testing including benchmarking
   * Best for preventing end-to-end regression and verifying overall correctness
   * Execution can be slow

Appropriate test coverage is an important part of code review.

## Security

Maintain appropriate security mindset for production serving. The project will establish a project email address for responsible disclosure of security issues that will be reviewed by the project maintainers. Prior to the first GA release we will formalize a security component and process. More details on security can be found in the [SECURITY.md](./SECURITY.md) file.

## Project Structure and Ownership

The repository contains the following deployable components.

  | Component | Language | Source | Description |
  |---|---|---|---|
  | **Dual-Pods Controller** | Go | `cmd/dual-pods-controller/`, `pkg/controller/dual-pods/` | Manages server-providing Pods (milestone 2) and launched vLLM instances (milestone 3) in reaction to server-requesting Pods. Handles binding, sleep/wake, and readiness relay. |
  | **Launcher-Populator Controller** | Go | `cmd/launcher-populator/`, `pkg/controller/launcher-populator/` | Proactively creates launcher pods on nodes based on `LauncherPopulationPolicy` CRDs. |
  | **Requester** | Go | `cmd/requester/`, `pkg/server/requester/` | Lightweight binary running in server-requesting Pods. Exposes SPI endpoints for GPU info and readiness relay. |
  | **Launcher** | Python | `inference_server/launcher/` | FastAPI service managing multiple vLLM subprocess instances via REST API. |
  | **Test Requester** | Go | `cmd/test-requester/` | Test binary simulating a requester (does not use real GPUs). |
  | **Test Server** | Go | `cmd/test-server/` | Test binary simulating a vLLM-like inference server. |
  | **Test Launcher** | Python | `dockerfiles/Dockerfile.launcher.cpu` | CPU-based launcher image for testing without GPUs. |

  The two controllers are deployed via a single Helm chart in `charts/fma-controllers/`.

### Core Organization (`llm-d-incubation/llm-d-fast-model-actuation`)

This is an **incubating component** in the llm-d ecosystem, focused on fast model actuation techniques.

#### Directory Structure

* **`api/fma/v1alpha1/`**: Custom Resource Definitions (CRDs) and Go types
  * `inferenceserverconfig_types.go`: InferenceServerConfig CRD
  * `launcherconfig_types.go`: LauncherConfig CRD
  * `launcherpopulationpolicy_types.go`: LauncherPopulationPolicy CRD

* **`cmd/`**: Main applications
  * `dual-pods-controller/`: Controller managing server-providing Pods
  * `launcher-populator/`: Controller managing launcher pod population
  * `requester/`: Requester binary for server-requesting Pods
  * `test-requester/`: Test requester (does not use real GPUs)
  * `test-server/`: Test binary simulating a vLLM-like inference server

* **`charts/`**: Helm charts for deployment
  * `fma-controllers/`: Unified Helm chart for both controllers

* **`config/`**: Kubernetes configurations (CRDs, examples, and more — see [cluster-sharing docs](docs/cluster-sharing.md) for recent extensions)

* **`inference_server/`**: Python-based inference server components
  * `launcher/`: vLLM instance launcher (persistent management process)
  * `benchmark/`: Benchmarking tools and scenarios

* **`docs/`**: Documentation (see [`docs/README.md`](docs/README.md) for full index)

* **`test/e2e/`**: End-to-end test scripts
  * `run.sh`: Standard dual-pods E2E test
  * `run-launcher-based.sh`: Launcher-based E2E test

* **`dockerfiles/`**: Container image definitions
  * `Dockerfile.launcher.cpu`: CPU-based launcher image for testing without GPUs
  * `Dockerfile.launcher.benchmark`: GPU-based launcher image (the real deal)
  * `Dockerfile.requester`: Requester application image

### Component Ownership

* **Maintainers** are listed in the [OWNERS](OWNERS) file. The file follows [Kubernetes OWNERS conventions](https://www.kubernetes.dev/docs/guide/owners/) for future Prow compatibility but is not currently consumed by automation. Additional OWNERS files can be added per-directory as the project grows.
* **Contributors** can become maintainers through consistent, quality contributions

### Incubation Status

FMA is currently in the **llm-d-incubation** organization, which means:

* **Rapid iteration**: Greater freedom for testing new ideas and approaches
* **Components may change significantly** as we learn
* **Best effort support**: Not yet ready for production use
* **Graduation path**: Working toward integration with core llm-d components

### Graduation

Graduation criteria are defined by the llm-d organization (not this repo). This repo tracks its progress toward meeting those criteria. See the llm-d organization documentation for details.
