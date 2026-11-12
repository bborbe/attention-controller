# Definition of Done

A prompt is complete when ALL of the following are true:

## Build

- [ ] `make precommit` passes (format + lint + test + security checks)
- [ ] No new linting warnings or errors

## Code Quality

- [ ] No `//nolint` without explanation
- [ ] Follows existing code patterns in the file being modified

## Tests

- [ ] New functions have tests
- [ ] Existing tests still pass
- [ ] Tests use Ginkgo/Gomega conventions
- [ ] Counterfeiter for mocks (`mocks/` dir)

## Style

- [ ] Functions over classes for stateless operations
- [ ] Error handling follows `github.com/bborbe/errors` patterns
- [ ] No absolute paths — all paths relative or using standard lib

## Release

- [ ] `make e2e` is green before the pull request is opened — the merge cuts the tag, so this is the only gate
- [ ] A pre-existing failing spec is named in the pull request body, not silently absorbed

See `docs/release-process.md` for the release path, the red-suite rule and how to read the spec count.
