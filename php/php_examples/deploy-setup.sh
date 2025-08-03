#!/bin/bash

# Deployment script for go-pugleaf PHP Frontend
# This script handles the configuration setup for rsync-friendly deployment

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

print_status() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Check if we're in the right directory
if [ ! -f "config.sample.php" ]; then
    print_error "config.sample.php not found. Please run this script from the php/ directory."
    exit 1
fi

# Check if config.inc.php already exists
if [ -f "config.inc.php" ]; then
    print_warning "config.inc.php already exists."
    read -p "Do you want to overwrite it? (y/N): " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        print_status "Keeping existing config.inc.php"
        exit 0
    fi
fi

# Copy config template
print_status "Creating config.inc.php from template..."
cp config.sample.php config.inc.php

# Make it writable for editing
chmod 644 config.inc.php

print_status "Configuration file created successfully!"
echo
echo "Next steps:"
echo "1. Edit config.inc.php to match your environment:"
echo "   - Update PUGLEAF_API_BASE to your backend URL"
echo "   - Set DEBUG_MODE to false for production"
echo "   - Configure other settings as needed"
echo
echo "2. The config.inc.php file is gitignored and rsync-safe"
echo "3. Your deployment process won't overwrite local configuration"
echo
print_warning "Remember to configure your backend URL in config.inc.php!"

# Optionally prompt for basic configuration
echo
read -p "Would you like to configure basic settings now? (y/N): " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
    echo
    read -p "Enter your backend API URL (e.g., http://localhost:8080/api/v1): " API_URL
    read -p "Enter your backend web URL (e.g., http://localhost:8080): " WEB_URL
    read -p "Enable debug mode? (y/N): " -n 1 -r DEBUG_CHOICE
    echo

    if [ ! -z "$API_URL" ]; then
        sed -i "s|http://localhost:8080/api/v1|$API_URL|g" config.inc.php
        print_status "Updated API URL to: $API_URL"
    fi

    if [ ! -z "$WEB_URL" ]; then
        sed -i "s|http://localhost:8080|$WEB_URL|g" config.inc.php
        print_status "Updated web URL to: $WEB_URL"
    fi

    if [[ $DEBUG_CHOICE =~ ^[Yy]$ ]]; then
        sed -i "s/define('DEBUG_MODE', true);/define('DEBUG_MODE', true);/" config.inc.php
        print_status "Debug mode enabled"
    else
        sed -i "s/define('DEBUG_MODE', true);/define('DEBUG_MODE', false);/" config.inc.php
        print_status "Debug mode disabled (production mode)"
    fi

    echo
    print_status "Basic configuration completed!"
fi

print_status "Deployment setup complete!"
