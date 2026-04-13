# Contributing to OpenSaola

Thank you for your interest in contributing to OpenSaola! This document provides guidelines and instructions for contributing.

## Development Setup

### Prerequisites
- Go 1.24+
- Docker
- kubectl
- A Kubernetes cluster (Kind or Minikube for development)
- Helm 3

### Building
```bash
make build
make docker-build
```

### Running Tests
```bash
# Unit tests
make test

# End-to-end tests
make test-e2e
```

### Code Generation
If you modify CRD types in `api/v1/`, run:
```bash
make manifests generate
```

## Making Changes

### Fork and Branch
1. Fork the repo
2. Create a feature branch from main
3. Make changes
4. Submit a PR

### Code Style
- Run `go fmt ./...` and `go vet ./...`
- Follow standard Go conventions
- Write tests for new functionality
- Update documentation as needed

### Commit Messages
Use conventional commit format:
- `feat`: new feature
- `fix`: bug fix
- `docs`: documentation
- `refactor`: code refactoring
- `test`: adding tests
- `chore`: maintenance

### Pull Request Process
1. Ensure tests pass
2. Update documentation
3. Add description of changes
4. Request review

## Reporting Issues
Use GitHub Issues with appropriate labels.

## License
By contributing, you agree that your contributions will be licensed under the Apache License 2.0.
