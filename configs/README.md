# Configuration Files | BRAINSTORMING !!!

This directory contains configuration files and templates for go-pugleaf.

## Files

- `server-config.example.json` - Example server configuration
- `providers.example.json` - Example NNTP provider configurations
- `database-config.example.json` - Example database settings

## Usage

Copy the example files and remove the `.example` extension, then customize for your environment.

## Production Deployment

- Use environment variables for sensitive data (passwords, keys)
- Store configuration files outside the application directory
- Use proper file permissions (600) for configuration files containing credentials
