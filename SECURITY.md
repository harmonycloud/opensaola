# Security Policy

## Supported Versions

| Version | Supported          |
| ------- | ------------------ |
| 1.5.x   | Yes |
| < 1.5   | No  |

## Reporting a Vulnerability

If you discover a security vulnerability in OpenSaola, please report it responsibly.

**Please do NOT open a public GitHub issue for security vulnerabilities.**

Instead, please send an email to: **security@opensaola.io**

### What to Include

- Description of the vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (if any)

### Response Timeline

- **Acknowledgment**: Within 48 hours
- **Initial assessment**: Within 1 week
- **Fix timeline**: Depends on severity, typically within 30 days for critical issues

### Disclosure Policy

- We will work with you to understand and address the issue
- We will provide credit to reporters who follow responsible disclosure
- We aim to release fixes before public disclosure

## Security Best Practices for Users

- Always use the latest supported version
- Follow RBAC best practices when deploying the operator
- Review ClusterRole permissions before installation
- Use network policies to restrict operator pod network access
