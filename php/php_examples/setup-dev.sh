#!/bin/bash

# Development environment setup for go-pugleaf PHP frontend
# This script sets up a local development environment with PHP built-in server

set -e

# Configuration
DEV_PORT=8080
PHP_VERSION="8.2"
BACKEND_URL="http://localhost:8080"  # Adjust to match your go-pugleaf backend

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
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

print_note() {
    echo -e "${BLUE}[NOTE]${NC} $1"
}

# Check if PHP is installed
if ! command -v php &> /dev/null; then
    print_error "PHP is not installed. Please install PHP ${PHP_VERSION} or later."
    print_note "On Ubuntu/Debian: sudo apt install php${PHP_VERSION}-cli php${PHP_VERSION}-curl php${PHP_VERSION}-json php${PHP_VERSION}-mbstring"
    exit 1
fi

# Check PHP version
PHP_ACTUAL_VERSION=$(php -r "echo PHP_VERSION;" | cut -d. -f1,2)
print_status "PHP version: $PHP_ACTUAL_VERSION"

# Check required PHP extensions
print_status "Checking PHP extensions..."
REQUIRED_EXTS=("curl" "json" "mbstring")
MISSING_EXTS=()

for ext in "${REQUIRED_EXTS[@]}"; do
    if ! php -m | grep -q "^$ext$"; then
        MISSING_EXTS+=("$ext")
    fi
done

if [ ${#MISSING_EXTS[@]} -ne 0 ]; then
    print_error "Missing PHP extensions: ${MISSING_EXTS[*]}"
    print_note "Install with: sudo apt install $(printf 'php%s-%s ' $(printf '%s\n' "${MISSING_EXTS[@]}" | sed "s/^/${PHP_VERSION}-/"))"
    exit 1
fi

print_status "All required PHP extensions are installed"

# Check if we're in the right directory
if [ ! -f "php/index.php" ]; then
    print_error "php/index.php not found. Please run this script from the go-pugleaf project root."
    exit 1
fi

# Create a development configuration
print_status "Creating development configuration..."
cat > php/dev-config.php << EOF
<?php
// Development configuration
// This file is included by index.php when in development mode

// Error reporting for development
error_reporting(E_ALL);
ini_set('display_errors', 1);
ini_set('log_errors', 1);

// Development-specific constants
define('DEV_MODE', true);
define('DEBUG_MODE', true);

// Backend URL for development
const API_BASE_URL = '${BACKEND_URL}';

// Cache settings for development (shorter TTL)
const CACHE_TTL = 60; // 1 minute for development

// Template debugging
const TEMPLATE_DEBUG = true;

// CORS headers for development
if (isset(\$_SERVER['HTTP_ORIGIN'])) {
    header('Access-Control-Allow-Origin: ' . \$_SERVER['HTTP_ORIGIN']);
    header('Access-Control-Allow-Credentials: true');
    header('Access-Control-Max-Age: 86400');
}

if (\$_SERVER['REQUEST_METHOD'] == 'OPTIONS') {
    if (isset(\$_SERVER['HTTP_ACCESS_CONTROL_REQUEST_METHOD'])) {
        header('Access-Control-Allow-Methods: GET, POST, OPTIONS');
    }
    if (isset(\$_SERVER['HTTP_ACCESS_CONTROL_REQUEST_HEADERS'])) {
        header('Access-Control-Allow-Headers: ' . \$_SERVER['HTTP_ACCESS_CONTROL_REQUEST_HEADERS']);
    }
    exit(0);
}

// Development logging
function dev_log(\$message, \$context = []) {
    \$timestamp = date('Y-m-d H:i:s');
    \$log_entry = "[{\$timestamp}] {\$message}";
    if (!empty(\$context)) {
        \$log_entry .= ' ' . json_encode(\$context);
    }
    error_log(\$log_entry . PHP_EOL, 3, 'php/dev.log');
}

// Enable development features
\$GLOBALS['DEV_MODE'] = true;
EOF

# Modify index.php to include dev config
if ! grep -q "dev-config.php" php/index.php; then
    print_status "Adding development configuration to index.php..."
    sed -i '1a\\n// Include development configuration if available\nif (file_exists(__DIR__ . "/dev-config.php")) {\n    require_once __DIR__ . "/dev-config.php";\n}' php/index.php
fi

# Create a simple start script
print_status "Creating development start script..."
cat > start-dev.sh << 'EOF'
#!/bin/bash

# Start script for go-pugleaf PHP frontend development server

DEV_PORT=8080
DEV_HOST="localhost"

# Function to cleanup on exit
cleanup() {
    echo
    echo "Shutting down development server..."
    kill $PHP_PID 2>/dev/null || true
    exit 0
}

# Set up trap for cleanup
trap cleanup SIGINT SIGTERM

# Start PHP development server
echo "Starting go-pugleaf PHP development server..."
echo "Server will be available at: http://${DEV_HOST}:${DEV_PORT}"
echo "Press Ctrl+C to stop the server"
echo

cd php
php -S ${DEV_HOST}:${DEV_PORT} -t . index.php &
PHP_PID=$!

# Wait for the server to start
sleep 2

# Check if server started successfully
if kill -0 $PHP_PID 2>/dev/null; then
    echo "Development server started successfully!"
    echo
    echo "Available routes:"
    echo "  http://${DEV_HOST}:${DEV_PORT}/                     - Home page"
    echo "  http://${DEV_HOST}:${DEV_PORT}/groups               - Groups list"
    echo "  http://${DEV_HOST}:${DEV_PORT}/group/test.group     - Group view"
    echo "  http://${DEV_HOST}:${DEV_PORT}/article/123          - Article view"
    echo "  http://${DEV_HOST}:${DEV_PORT}/search?q=test        - Search"
    echo "  http://${DEV_HOST}:${DEV_PORT}/stats                - Statistics"
    echo "  http://${DEV_HOST}:${DEV_PORT}/help                 - Help page"
    echo
    echo "Development features enabled:"
    echo "  - Error reporting and display"
    echo "  - Debug mode"
    echo "  - Short cache TTL (1 minute)"
    echo "  - Development logging to php/dev.log"
    echo

    # Wait for PHP server to finish
    wait $PHP_PID
else
    echo "Failed to start development server!"
    exit 1
fi
EOF

chmod +x start-dev.sh

# Create a stop script
print_status "Creating development stop script..."
cat > stop-dev.sh << 'EOF'
#!/bin/bash

# Stop the PHP development server
echo "Stopping PHP development server..."

# Find and kill PHP development server processes
pkill -f "php -S.*8080" || true

echo "Development server stopped."
EOF

chmod +x stop-dev.sh

# Create development .htaccess for Apache users
print_status "Creating development .htaccess..."
cat > php/.htaccess-dev << 'EOF'
# Development .htaccess for go-pugleaf PHP frontend
# Copy this to .htaccess if using Apache instead of PHP built-in server

RewriteEngine On

# Enable error display for development
php_flag display_errors On
php_flag log_errors On
php_value error_log /tmp/php_errors.log

# Development security headers (less restrictive)
Header always set X-Content-Type-Options nosniff
Header always set X-Frame-Options SAMEORIGIN
Header always set X-XSS-Protection "1; mode=block"

# CORS for development
Header always set Access-Control-Allow-Origin "*"
Header always set Access-Control-Allow-Methods "GET, POST, OPTIONS"
Header always set Access-Control-Allow-Headers "Content-Type, Authorization"

# Handle OPTIONS requests
RewriteCond %{REQUEST_METHOD} OPTIONS
RewriteRule ^(.*)$ $1 [R=204,L]

# Route all requests to index.php
RewriteCond %{REQUEST_FILENAME} !-f
RewriteCond %{REQUEST_FILENAME} !-d
RewriteRule ^(.*)$ index.php?route=$1 [QSA,L]

# Prevent access to sensitive files
<FilesMatch "\.(log|md|txt|conf)$">
    Order allow,deny
    Deny from all
</FilesMatch>
EOF

# Create a simple test script
print_status "Creating test script..."
cat > test-frontend.sh << 'EOF'
#!/bin/bash

# Simple test script for go-pugleaf PHP frontend

TEST_URL="http://localhost:8080"
FAILED_TESTS=0

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m'

test_endpoint() {
    local endpoint=$1
    local expected_status=${2:-200}
    local description=$3

    echo -n "Testing $description... "

    response=$(curl -s -o /dev/null -w "%{http_code}" "$TEST_URL$endpoint")

    if [ "$response" -eq "$expected_status" ]; then
        echo -e "${GREEN}PASS${NC} ($response)"
    else
        echo -e "${RED}FAIL${NC} (got $response, expected $expected_status)"
        ((FAILED_TESTS++))
    fi
}

echo "Testing go-pugleaf PHP frontend..."
echo "Make sure the development server is running first!"
echo

# Test main endpoints
test_endpoint "/" 200 "Home page"
test_endpoint "/groups" 200 "Groups list"
test_endpoint "/group/test.group" 200 "Group view"
test_endpoint "/article/123" 200 "Article view"
test_endpoint "/search?q=test" 200 "Search"
test_endpoint "/stats" 200 "Statistics"
test_endpoint "/help" 200 "Help page"
test_endpoint "/nonexistent" 404 "404 error handling"

echo
if [ $FAILED_TESTS -eq 0 ]; then
    echo -e "${GREEN}All tests passed!${NC}"
    exit 0
else
    echo -e "${RED}$FAILED_TESTS test(s) failed!${NC}"
    exit 1
fi
EOF

chmod +x test-frontend.sh

# Create README for development
print_status "Creating development README..."
cat > DEV-README.md << 'EOF'
# Development Environment

This directory contains scripts and configuration for local development of the go-pugleaf PHP frontend.

## Quick Start

1. **Start the development server:**
   ```bash
   ./start-dev.sh
   ```

2. **Open your browser to:**
   ```
   http://localhost:8080
   ```

3. **Stop the server:**
   ```bash
   ./stop-dev.sh
   ```
   Or press `Ctrl+C` in the terminal running the server.

## Development Features

- **Error Display**: PHP errors are displayed in the browser
- **Debug Mode**: Additional debugging information
- **Short Cache TTL**: API responses cached for only 1 minute
- **Development Logging**: Logs written to `php/dev.log`
- **CORS Headers**: Enabled for cross-origin requests

## Testing

Run the test script to verify all endpoints:
```bash
./test-frontend.sh
```

## Configuration

Edit `php/dev-config.php` to modify development settings:
- Backend URL
- Cache settings
- Debug options
- CORS configuration

## File Structure

- `start-dev.sh` - Start development server
- `stop-dev.sh` - Stop development server
- `test-frontend.sh` - Test all endpoints
- `php/dev-config.php` - Development configuration
- `php/.htaccess-dev` - Apache configuration for development
- `php/dev.log` - Development log file

## Troubleshooting

### Server won't start
- Check if port 8080 is already in use: `lsof -i :8080`
- Make sure you're in the project root directory
- Verify PHP is installed: `php --version`

### Backend connection issues
- Update `BACKEND_URL` in `php/dev-config.php`
- Make sure your go-pugleaf backend is running
- Check the backend logs for API errors

### Permission issues
- Make sure the `php/` directory is writable
- Check that log files can be created: `touch php/dev.log`

## Production Deployment

When ready for production:
1. Remove or comment out the dev-config.php include in index.php
2. Use the production Nginx configuration
3. Follow the deployment guide in NGINX-SSL-DEPLOYMENT.md
EOF

# Final status
print_status "Development environment setup complete!"
echo
echo "Created files:"
echo "  - start-dev.sh (start development server)"
echo "  - stop-dev.sh (stop development server)"
echo "  - test-frontend.sh (test all endpoints)"
echo "  - php/dev-config.php (development configuration)"
echo "  - php/.htaccess-dev (Apache development config)"
echo "  - DEV-README.md (development documentation)"
echo
echo "To start developing:"
echo "  1. ./start-dev.sh"
echo "  2. Open http://localhost:${DEV_PORT} in your browser"
echo "  3. Edit files in the php/ directory"
echo "  4. Changes are live-reloaded automatically"
echo
print_warning "Make sure your go-pugleaf backend is running at ${BACKEND_URL}"
print_note "Edit php/dev-config.php to change the backend URL if needed"
