
# Project Proposal: cppm — A Lightweight C++ Project & Dependency Manager

## 1. Project Overview

Build a lightweight, developer-friendly CLI tool called **cppm (C++ Project Manager)**.

The primary goal is to simplify C++ project creation, dependency management, and CMake integration.

Currently, C++ developers often need to manually:
- Download libraries from GitHub.
- Clone and configure external repositories.
- Write `FetchContent` declarations.
- Call `find_package()`.
- Configure `target_link_libraries()`.
- Manage include directories and linking.
- Configure local modules and subprojects.
- Maintain dependency versions.

cppm should automate these operations.

The developer should be able to add a dependency with one command, and cppm should automatically configure the project and synchronize its CMake configuration.

**Core philosophy: Developers should manage dependencies, not write dependency-related CMake code.**

## 2. Main Objectives

1. Provide a simple and intuitive CLI.
2. Support GitHub repositories as first-class dependencies.
3. Support local C++ projects and modules.
4. Automatically generate and synchronize CMake configuration.
5. Manage dependency versions and reproducible builds.
6. Support transitive dependencies where possible.
7. Keep the generated configuration understandable and maintainable.
8. Avoid reinventing existing tools unnecessarily.

cppm should use existing technologies internally instead of implementing its own compiler, linker, or build system.

## 3. Technology Stack

Use the following technologies:

- **Implementation:** Modern C++ (C++20 or newer).
- **Build system:** CMake.
- **Build backend:** Ninja (when available).
- **Git integration:** Git CLI.
- **Configuration format:** JSON.
- **Dependency acquisition:** Git repositories and CMake FetchContent.
- **CLI parsing:** CLI11 or another lightweight C++ CLI library.
- **JSON parsing:** nlohmann-json.

Keep the architecture modular and avoid unnecessary dependencies.

## 4. Project Structure

A cppm-managed project should look like this:

```text
MyProject/
├── cppm.json
├── cppm.lock
├── CMakeLists.txt
├── CMakePresets.json
├── src/
│   └── main.cpp
├── include/
├── modules/
│   └── MyModule/
├── .cppm/
│   ├── dependencies/
│   └── generated/
│       └── cppm_dependencies.cmake
└── build/
```

The `.cppm` directory should contain generated and downloaded files and should normally be excluded from Git.

The lockfile and project manifest should be committed to Git.

## 5. CLI Commands

### Project initialization

```bash
cppm init MyProject
```

Create a new C++ project with:
- `cppm.json`
- `cppm.lock`
- `CMakeLists.txt`
- `CMakePresets.json`
- `src/main.cpp`

Support initialization in the current directory:

```bash
cppm init .
```

### Add dependencies

Support GitHub shorthand:

```bash
cppm add github:fmtlib/fmt
cppm add github:nlohmann/json
cppm add github:gabime/spdlog
```

Support pinned versions:

```bash
cppm add github:fmtlib/fmt@11.1.4
```

Support specific Git branches, tags, and commits.

Support local dependencies:

```bash
cppm add local:../MyLibrary
```

Adding a dependency should:
1. Resolve the repository.
2. Determine its available version or requested Git reference.
3. Inspect its CMake configuration when possible.
4. Add it to `cppm.json`.
5. Resolve its CMake target information.
6. Update `cppm.lock`.
7. Regenerate the managed CMake configuration.

### Remove dependencies

```bash
cppm remove fmt
```

Remove the dependency from the manifest and synchronize the generated configuration.

### Synchronize dependencies

```bash
cppm sync
```

This command should:
- Read `cppm.json`.
- Resolve dependency versions.
- Download missing repositories.
- Reuse existing downloads when possible.
- Update `cppm.lock`.
- Generate the CMake dependency integration file.
- Detect missing or invalid dependencies.

### Update dependencies

```bash
cppm update
```

Update dependencies according to the project's version constraints and regenerate the lockfile.

### Build project

```bash
cppm build
```

Automatically:
1. Run dependency synchronization when needed.
2. Configure CMake.
3. Generate build files.
4. Compile the project.

Also support:

```bash
cppm build --release
cppm build --debug
cppm build --clean
```

### Additional commands

```bash
cppm list
cppm info fmt
cppm doctor
cppm clean
```

`doctor` should diagnose missing compilers, CMake, Ninja, Git, and invalid project configuration.

## 6. Manifest Design

Use a simple JSON manifest.

Example `cppm.json`:

```json
{
  "name": "MyProject",
  "version": "1.0.0",
  "language": "cxx20",
  "dependencies": {
    "fmt": {
      "source": "github",
      "repository": "fmtlib/fmt",
      "version": "11.1.4"
    },
    "json": {
      "source": "github",
      "repository": "nlohmann/json",
      "version": "3.11.3"
    },
    "MyLibrary": {
      "source": "local",
      "path": "../MyLibrary"
    }
  }
}
```

Design the schema to support future dependency sources and additional package metadata.

## 7. Lockfile

Create a `cppm.lock` file to guarantee reproducible dependency resolution.

It should store:
- Dependency identifiers.
- Repository URLs.
- Resolved Git commits.
- Selected versions and references.
- Dependency relationships.
- Relevant package metadata.

A version tag alone is not sufficient for reproducibility. Resolve Git tags to immutable commit hashes and store those hashes in the lockfile.

Running `cppm sync` with an existing valid lockfile should preserve locked versions.

Only `cppm update` should intentionally update dependencies.

## 8. CMake Integration

This is one of the most important parts of cppm.

Developers should not need to manually write dependency-related CMake code.

cppm should generate:

`.cppm/generated/cppm_dependencies.cmake`

For example:

```cmake
include(FetchContent)

FetchContent_Declare(
    fmt
    GIT_REPOSITORY https://github.com/fmtlib/fmt.git
    GIT_TAG <resolved-commit>
)

FetchContent_MakeAvailable(fmt)

FetchContent_Declare(
    json
    GIT_REPOSITORY https://github.com/nlohmann/json.git
    GIT_TAG <resolved-commit>
)

FetchContent_MakeAvailable(json)
```

The generated file should also handle linking dependency targets to the appropriate application targets.

For projects created by cppm, generate a minimal `CMakeLists.txt`:

```cmake
cmake_minimum_required(VERSION 3.21)

project(MyProject LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

include(.cppm/generated/cppm_dependencies.cmake)

add_executable(MyProject
    src/main.cpp
)

cppm_link_dependencies(MyProject)
```

Do not blindly assume that a GitHub repository name matches its exported CMake target name.

Implement dependency target discovery using available CMake metadata and explicit package configuration when automatic discovery is impossible.

Support both:

- Automatic target detection.
- Explicit target declarations in dependency metadata.

Keep generated files separate from user-maintained files. Never overwrite custom user CMake configuration during synchronization.

## 9. Dependency Types

Initially support three dependency types.

### A. GitHub dependencies

Download source code from public Git repositories.

Support tags, branches, and commit hashes.

Prefer immutable commit hashes in the lockfile.

### B. Local dependencies

Allow developers to include local C++ projects.

Example:

```bash
cppm add local:../MyLibrary
```

Use `add_subdirectory()` or another suitable CMake integration mechanism.

Support editable local dependencies without copying their source code unnecessarily.

### C. CMake-compatible libraries

Support repositories that expose standard CMake targets.

Detect their exported targets whenever possible.

For libraries that do not expose CMake integration, allow explicit package configuration.

## 10. Important Engineering Requirements

- Use safe Git subprocess execution.
- Never construct shell commands by blindly concatenating user input.
- Handle network failures and invalid repositories gracefully.
- Cache downloaded repositories.
- Avoid downloading the same dependency repeatedly.
- Detect dependency conflicts.
- Detect circular local dependencies.
- Support Linux, Windows, and macOS.
- Use atomic writes for manifests and lockfiles.
- Provide meaningful CLI error messages.
- Keep dependency resolution deterministic.
- Never modify user-owned CMake sections automatically.
- Make synchronization idempotent.

## 11. Architecture

Separate the application into independent components:

```text
src/
├── cli/
│   ├── commands/
│   └── argument_parser.cpp
├── project/
│   ├── project_manager.cpp
│   └── manifest.cpp
├── dependency/
│   ├── dependency_resolver.cpp
│   ├── github_provider.cpp
│   ├── local_provider.cpp
│   └── lockfile.cpp
├── cmake/
│   ├── cmake_generator.cpp
│   └── target_detector.cpp
├── git/
│   └── git_client.cpp
├── build/
│   └── build_manager.cpp
└── main.cpp
```

Use clear interfaces between components.

Avoid putting all functionality into one large class.

## 12. MVP Development Plan

Implement the project incrementally.

### Phase 1: CLI and project initialization
- CLI argument parsing.
- `cppm init`.
- Manifest creation.
- Lockfile creation.
- Basic project template.

### Phase 2: GitHub dependency management
- `cppm add`.
- Git repository cloning.
- Version/tag resolution.
- Dependency caching.
- `cppm remove`.

### Phase 3: CMake integration
- Generate dependency CMake files.
- Detect exported targets.
- Link dependencies automatically.
- Support local projects.
- Implement `cppm sync`.

### Phase 4: Build integration
- `cppm build`.
- CMake configuration.
- Ninja integration.
- Debug and release profiles.

### Phase 5: Reliability
- Lockfile consistency.
- Dependency conflict detection.
- Error handling.
- Cross-platform support.
- Tests and documentation.

Do not implement all features at once. Deliver a working, tested MVP before expanding.

## 13. Expected Developer Experience

The final experience should look like this:

```bash
cppm init MyApp

cd MyApp

cppm add github:fmtlib/fmt@11.1.4

cppm add github:nlohmann/json@3.11.3

cppm add local:../MyLibrary

cppm sync

cppm build
```

The developer writes application code:

```cpp
#include <fmt/core.h>
#include <nlohmann/json.hpp>

int main()
{
    nlohmann::json data = {
        {"message", "Hello from cppm"}
    };

    fmt::print("{}\n", data.dump(4));
}
```

No manual dependency-related CMake configuration should be necessary.

## 14. Instructions to the AI Coding Assistant

Before implementing:

1. Analyze the proposed architecture.
2. Identify potential technical limitations.
3. Suggest improvements where appropriate.
4. Explain how CMake FetchContent, Git, and dependency resolution should interact.
5. Establish the manifest and lockfile schemas.
6. Define the MVP scope.

Then implement incrementally.

For each phase:
- Write the implementation.
- Add unit and integration tests.
- Build and run tests.
- Fix errors before proceeding.
- Update documentation.

Do not create unnecessary abstractions or implement an entire package registry in the MVP.

**The ultimate goal is a lightweight, reliable, cross-platform C++ project manager that makes adding GitHub repositories and local C++ modules as simple as running one CLI command, while keeping CMake working automatically behind the scenes.**
