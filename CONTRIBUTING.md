## Contributing Guidelines

Thank you for your interest in contributing to llm-d Fast Model Actuation (FMA). Community involvement is highly valued and crucial for the project's growth and success. The FMA project accepts contributions via GitHub pull requests. This outlines the process to help get your contribution accepted.

To ensure a clear direction and cohesive vision for the project, the project leads have the final decision on all contributions. However, these guidelines outline how you can contribute effectively to FMA.

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

We follow a **lazy consensus** approach: changes proposed by people with responsibility for a problem, without disagreement from others, within a bounded time window of review by their peers, should be accepted.

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

The FMA project includes several components that require different testing approaches:

### Testing FMA Components

#### 1. Dual-Pods Controller Testing

The dual-pods controller manages server-providing Pods in reaction to server-requesting Pods:

* **Unit tests**: Test controller logic in `cmd/dual-pods-controller/`
* **Integration tests**: Verify Pod creation, deletion, and lifecycle management
* **E2E tests**: Run the full controller in a Kubernetes cluster (kind or OpenShift)
  * Use `test/e2e/run.sh` for end-to-end testing
  * Verify server-requesting Pods trigger server-providing Pod creation
  * Test resource allocation and GPU assignment

#### 2. Launcher Testing

The vLLM instance launcher is a persistent management process written in Python:

* **Unit tests**: Located in `inference_server/launcher/tests/`
  * `test_launcher.py`: Tests launcher logic and vLLM instance management
  * `test_gputranslator.py`: Tests GPU resource translation
* **Integration tests**: Test launcher with actual vLLM instances
  * Verify model loading and unloading
  * Test sleep/wake functionality
  * Validate model swapping capabilities
* **Benchmark tests**: Use `inference_server/benchmark/` for performance testing
  * Run scenarios defined in `scenarios.py`
  * Measure startup latency and model swap times

#### 3. Launcher-Populator Controller Testing

The launcher-populator controller ensures the right number of launcher pods exist on each node:

* **Unit tests**: Test controller logic in `cmd/launcher-populator/`
* **Integration tests**: Verify LauncherConfig and LauncherPopulationPolicy handling
* **E2E tests**: Validate launcher pod distribution across nodes
  * Use `test/e2e/run-launcher-based.sh` for launcher-based testing

#### 4. Custom Resource Definitions (CRDs)

Test the three CRDs defined in `config/crd/`:

* **InferenceServerConfig**: Verify server configuration properties
* **LauncherConfig**: Test launcher process configuration
* **LauncherPopulationPolicy**: Validate launcher pod population rules

### Running Tests

**Go tests:**

```bash
make test
```

**Python tests:**

```bash
cd inference_server/launcher
python -m pytest tests/
```

**E2E tests:**

```bash
# Standard dual-pods test
./test/e2e/run.sh

# Launcher-based test
./test/e2e/run-launcher-based.sh
```

**Benchmark tests:**

```bash
cd inference_server/benchmark
python benchmark_base.py
```

### Code Review Requirements

* **All code changes** must be submitted as pull requests (no direct pushes)
* **All changes** must be reviewed and approved by a maintainer other than the author
* **All repositories** must gate merges on compilation and passing tests
* **All experimental features** must be off by default and require explicit opt-in

### Commit and Pull Request Style

* **Pull requests** should describe the problem succinctly
* **Rebase and squash** before merging
* **Use minimal commits** and break large changes into distinct commits
* **Commit messages** should have:
  * Short, descriptive titles
  * Description of why the change was needed
  * Enough detail for someone reviewing git history to understand the scope
* **DCO Sign-off**: All commits must include a valid DCO sign-off line (`Signed-off-by: Name <email@domain.com>`)
  * Add automatically with `git commit -s`
  * See [PR_SIGNOFF.md](PR_SIGNOFF.md) for configuration details
  * Required for all contributions per [Developer Certificate of Origin](https://developercertificate.org/)

## Code Organization and Ownership

### Components and Maintainers

* **Components** are the primary unit of code organization (repo scope or directory/package/module within a repo)
* **Maintainers** own components and approve changes
* **Contributors** can become maintainers through sufficient evidence of contribution
* Code ownership is reflected in [OWNERS files](https://go.k8s.io/owners) consistent with Kubernetes project conventions

### Experimental Features in FMA

As an incubating component, FMA encourages fast iteration and exploration with these constraints:

1. **Clear identification** as experimental in code and documentation
2. **Default to off** and require explicit enablement for experimental features
3. **Best effort support** only
4. **Removal if unmaintained** with no one to move it forward
5. **No stigma** to experimental or incubating status

**Naming convention**: Experimental flags must include `experimental` in name (e.g., `--experimental-model-swap-v2=true`)

When adding experimental features:

1. Open pull request with clear experimental designation
2. Maintainer reviews and enforces "off-by-default" gating
3. Provide tests for both on/off states
4. Document the experimental nature in code comments and user documentation
5. When graduating a feature, default to on and remove conditional logic after one release

## API Changes and Deprecation

* **No breaking changes**: Once an API/protocol is in GA release (non-experimental), it cannot be removed or behavior changed
* **Includes**: All protocols, API endpoints, internal APIs, command line flags/arguments
* **Exception**: Bug fixes that don't impact significant number of consumers (As the project matures, we will be stricter about such changes - Hyrum's Law is real)
* **Versioning**: All protocols and APIs should be versionable with clear forward and backward compatibility requirements. A new version may change behavior and fields.
* **Documentation**: All APIs must have documented specs describing expected behavior

## Testing Requirements

We use three tiers of testing:

1. **Unit tests**: Fast verification of code parts, testing different arguments
   * Best for fast verification of parts of code, testing different arguments
   * Doesn't cover interactions between code
2. **Integration tests**: Testing protocols between components and built artifacts
   * Best for testing protocols and agreements between components
   * May not model interactions between components as they are deployed
3. **End-to-end (e2e) tests**: Whole system testing including benchmarking
   * Best for preventing end to end regression and verifying overall correctness
   * Execution can be slow

Strong e2e coverage is required for deployed systems to prevent performance regression. Appropriate test coverage is an important part of code review.

## Security

Maintain appropriate security mindset for production serving. The project will establish a project email address for responsible disclosure of security issues that will be reviewed by the project maintainers. Prior to the first GA release we will formalize a security component and process.

## Project Structure

The FMA repository is organized as follows:

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
  * `requester/`: Example requester application
  * `test-requester/`: Test requester with GPU allocation
  * `test-server/`: Test server application

* **`charts/`**: Helm charts for deployment
  * `dual-pods-controller/`: Helm chart for dual-pods controller
  * `launcher-populator/`: Helm chart for launcher-populator controller

* **`config/`**: Kubernetes configurations
  * `crd/`: CRD YAML definitions
  * `examples/`: Example configurations and deployments

* **`inference_server/`**: Python-based inference server components
  * `launcher/`: vLLM instance launcher (persistent management process)
  * `benchmark/`: Benchmarking tools and scenarios

* **`docs/`**: Documentation
  * `dual-pods.md`: Dual-pods architecture documentation
  * `launcher.md`: Launcher component documentation
  * `e2e-recipe.md`: End-to-end testing guide
  * `local-test.md`: Local testing instructions

* **`test/e2e/`**: End-to-end test scripts
  * `run.sh`: Standard dual-pods E2E test
  * `run-launcher-based.sh`: Launcher-based E2E test

* **`dockerfiles/`**: Container image definitions
  * `Dockerfile.launcher.cpu`: CPU-based launcher image
  * `Dockerfile.launcher.benchmark`: Benchmark launcher image
  * `Dockerfile.requester`: Requester application image

### Component Ownership

* **Maintainers** are listed in the [OWNERS](OWNERS) file
* **Contributors** can become maintainers through consistent, quality contributions
* Code ownership follows Kubernetes project conventions with OWNERS files

### Incubation Status

FMA is currently in the **llm-d-incubation** organization, which means:

* **Rapid iteration**: Greater freedom for testing new ideas and approaches
* **Experimental features**: Components may change significantly as we learn
* **Best effort support**: Not yet ready for production use
* **Graduation path**: Working toward integration with core llm-d components

### Graduation Criteria

To graduate to the core `llm-d` organization, FMA must demonstrate:

1. **Stability**: Proven reliability in test environments
2. **Performance**: Measurable improvements in model actuation speed
3. **Documentation**: Complete user and developer documentation
4. **Testing**: Comprehensive unit, integration, and E2E test coverage
5. **Community adoption**: Active use and feedback from early adopters
6. **API maturity**: Stable APIs ready for production use
