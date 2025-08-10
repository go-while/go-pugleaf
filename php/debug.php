<?php
/**
 * Diagnostic script for go-pugleaf PHP Frontend
 * Upload this file to your web server to diagnose 500 errors
 */

// Enable error reporting
error_reporting(E_ALL);
ini_set('display_errors', 1);
ini_set('log_errors', 1);

echo "<h1>go-pugleaf PHP Frontend Diagnostics</h1>";
echo "<pre>";

// Check PHP version
echo "PHP Version: " . phpversion() . "\n";
echo "PHP SAPI: " . php_sapi_name() . "\n";
echo "Server Software: " . ($_SERVER['SERVER_SOFTWARE'] ?? 'Unknown') . "\n";
echo "Document Root: " . ($_SERVER['DOCUMENT_ROOT'] ?? 'Unknown') . "\n";
echo "Script Path: " . __FILE__ . "\n";
echo "Current Directory: " . getcwd() . "\n";
echo "\n";

// Check required extensions
echo "=== PHP Extensions ===\n";
$required_extensions = ['curl', 'json', 'mbstring', 'session'];
foreach ($required_extensions as $ext) {
    $status = extension_loaded($ext) ? "✓ LOADED" : "✗ MISSING";
    echo "$ext: $status\n";
}
echo "\n";

// Check file permissions and existence
echo "=== File System Checks ===\n";
$files_to_check = [
    'index.php',
    'config.inc.php',
    'config.sample.php',
    'includes/router.php',
    'includes/api_client.php',
    'includes/template_engine.php',
    'includes/utils.php',
    'includes/controllers/BaseController.php',
    'includes/controllers/HomeController.php',
    'templates/base.php',
    'templates/home.php'
];

foreach ($files_to_check as $file) {
    if (file_exists($file)) {
        $perms = substr(sprintf('%o', fileperms($file)), -4);
        $size = filesize($file);
        echo "$file: ✓ EXISTS (perms: $perms, size: {$size}b)\n";
    } else {
        echo "$file: ✗ MISSING\n";
    }
}
echo "\n";

// Check directory permissions
echo "=== Directory Permissions ===\n";
$dirs_to_check = ['.', 'includes', 'includes/controllers', 'templates'];
foreach ($dirs_to_check as $dir) {
    if (is_dir($dir)) {
        $perms = substr(sprintf('%o', fileperms($dir)), -4);
        $readable = is_readable($dir) ? "✓" : "✗";
        echo "$dir/: EXISTS (perms: $perms, readable: $readable)\n";
    } else {
        echo "$dir/: ✗ MISSING\n";
    }
}
echo "\n";

// Test configuration loading
echo "=== Configuration Test ===\n";
if (file_exists('config.inc.php')) {
    echo "config.inc.php: ✓ EXISTS\n";
    try {
        ob_start();
        include 'config.inc.php';
        $config_output = ob_get_clean();

        if (!empty($config_output)) {
            echo "Config output (should be empty): " . htmlspecialchars($config_output) . "\n";
        }

        // Check if constants are defined
        $constants = ['PUGLEAF_API_BASE', 'PUGLEAF_WEB_BASE', 'DEBUG_MODE'];
        foreach ($constants as $const) {
            if (defined($const)) {
                $value = constant($const);
                if (is_bool($value)) {
                    $value = $value ? 'true' : 'false';
                }
                echo "$const: ✓ DEFINED = '$value'\n";
            } else {
                echo "$const: ✗ NOT DEFINED\n";
            }
        }
    } catch (Exception $e) {
        echo "Config loading error: " . $e->getMessage() . "\n";
    } catch (ParseError $e) {
        echo "Config parse error: " . $e->getMessage() . "\n";
    }
} else {
    echo "config.inc.php: ✗ MISSING\n";
}
echo "\n";

// Test include files
echo "=== Include Files Test ===\n";
$include_files = [
    'includes/router.php',
    'includes/api_client.php',
    'includes/template_engine.php',
    'includes/utils.php'
];

foreach ($include_files as $file) {
    if (file_exists($file)) {
        try {
            ob_start();
            include_once $file;
            $include_output = ob_get_clean();
            if (!empty($include_output)) {
                echo "$file: ⚠ HAS OUTPUT: " . htmlspecialchars(substr($include_output, 0, 100)) . "\n";
            } else {
                echo "$file: ✓ LOADED OK\n";
            }
        } catch (Exception $e) {
            echo "$file: ✗ ERROR: " . $e->getMessage() . "\n";
        } catch (ParseError $e) {
            echo "$file: ✗ PARSE ERROR: " . $e->getMessage() . "\n";
        }
    } else {
        echo "$file: ✗ MISSING\n";
    }
}
echo "\n";

// Test class loading
echo "=== Class Loading Test ===\n";
$classes = ['SimpleRouter', 'PugleafApiClient', 'TemplateEngine'];
foreach ($classes as $class) {
    if (class_exists($class)) {
        echo "$class: ✓ AVAILABLE\n";
    } else {
        echo "$class: ✗ NOT FOUND\n";
    }
}
echo "\n";

// Test basic functionality
echo "=== Basic Functionality Test ===\n";
try {
    if (class_exists('PugleafApiClient') && defined('PUGLEAF_API_BASE')) {
        $api = new PugleafApiClient(PUGLEAF_API_BASE);
        echo "API Client: ✓ CREATED\n";
    } else {
        echo "API Client: ✗ CANNOT CREATE\n";
    }

    if (class_exists('TemplateEngine')) {
        $template = new TemplateEngine(__DIR__ . '/templates');
        echo "Template Engine: ✓ CREATED\n";
    } else {
        echo "Template Engine: ✗ CANNOT CREATE\n";
    }

    if (class_exists('SimpleRouter')) {
        $router = new SimpleRouter();
        echo "Router: ✓ CREATED\n";
    } else {
        echo "Router: ✗ CANNOT CREATE\n";
    }
} catch (Exception $e) {
    echo "Functionality test error: " . $e->getMessage() . "\n";
}
echo "\n";

// Show server variables
echo "=== Server Environment ===\n";
$server_vars = ['REQUEST_URI', 'SCRIPT_NAME', 'PATH_INFO', 'QUERY_STRING', 'REQUEST_METHOD', 'HTTP_HOST'];
foreach ($server_vars as $var) {
    $value = $_SERVER[$var] ?? 'NOT SET';
    echo "$var: $value\n";
}
echo "\n";

// Show loaded extensions
echo "=== All Loaded Extensions ===\n";
$extensions = get_loaded_extensions();
sort($extensions);
echo implode(', ', $extensions) . "\n";

echo "</pre>";

echo "<hr>";
echo "<h2>Next Steps</h2>";
echo "<ul>";
echo "<li>If any files are missing, upload them to the server</li>";
echo "<li>If config.inc.php is missing, copy config.sample.php to config.inc.php</li>";
echo "<li>Check file permissions (should be 644 for PHP files, 755 for directories)</li>";
echo "<li>If classes are not found, check that include files loaded properly</li>";
echo "<li>Check your web server error logs for more details</li>";
echo "</ul>";

echo "<p><strong>Web Server Error Logs:</strong> Check your hosting control panel or server logs for detailed PHP error messages.</p>";
?>
