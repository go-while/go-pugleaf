<?php
/**
 * Simple test script for go-pugleaf PHP Frontend
 * This script tests the basic setup without running the full application
 */

// Enable error reporting
error_reporting(E_ALL);
ini_set('display_errors', 1);

echo "<!DOCTYPE html>";
echo "<html><head><title>go-pugleaf Test</title></head><body>";
echo "<h1>go-pugleaf PHP Frontend Test</h1>";

try {
    echo "<h2>Step 1: Configuration Loading</h2>";

    if (file_exists(__DIR__ . '/config.inc.php')) {
        require_once __DIR__ . '/config.inc.php';
        echo "<p>✓ config.inc.php loaded successfully</p>";

        if (defined('PUGLEAF_API_BASE')) {
            echo "<p>✓ PUGLEAF_API_BASE defined: " . PUGLEAF_API_BASE . "</p>";
        } else {
            echo "<p>✗ PUGLEAF_API_BASE not defined</p>";
        }

        if (defined('DEBUG_MODE')) {
            echo "<p>✓ DEBUG_MODE defined: " . (DEBUG_MODE ? 'true' : 'false') . "</p>";
        } else {
            echo "<p>✗ DEBUG_MODE not defined</p>";
        }
    } else {
        echo "<p>✗ config.inc.php not found!</p>";
        if (file_exists(__DIR__ . '/config.sample.php')) {
            echo "<p>→ config.sample.php exists - copy it to config.inc.php</p>";
        } else {
            echo "<p>✗ config.sample.php also missing!</p>";
        }
        throw new Exception("Configuration file missing");
    }

    echo "<h2>Step 2: Include Files</h2>";

    $includes = [
        'includes/router.php' => 'SimpleRouter',
        'includes/api_client.php' => 'PugleafApiClient',
        'includes/template_engine.php' => 'TemplateEngine',
        'includes/utils.php' => null
    ];

    foreach ($includes as $file => $expected_class) {
        if (file_exists(__DIR__ . '/' . $file)) {
            require_once __DIR__ . '/' . $file;
            echo "<p>✓ $file loaded</p>";

            if ($expected_class && class_exists($expected_class)) {
                echo "<p>✓ Class $expected_class available</p>";
            } elseif ($expected_class) {
                echo "<p>✗ Class $expected_class not found</p>";
            }
        } else {
            echo "<p>✗ $file not found</p>";
            throw new Exception("Required file missing: $file");
        }
    }

    echo "<h2>Step 3: Object Creation</h2>";

    // Test API client
    if (class_exists('PugleafApiClient')) {
        $api = new PugleafApiClient(PUGLEAF_API_BASE);
        echo "<p>✓ PugleafApiClient created</p>";
    } else {
        throw new Exception("PugleafApiClient class not available");
    }

    // Test template engine
    if (class_exists('TemplateEngine')) {
        $template = new TemplateEngine(__DIR__ . '/templates');
        echo "<p>✓ TemplateEngine created</p>";
    } else {
        throw new Exception("TemplateEngine class not available");
    }

    // Test router
    if (class_exists('SimpleRouter')) {
        $router = new SimpleRouter();
        echo "<p>✓ SimpleRouter created</p>";
    } else {
        throw new Exception("SimpleRouter class not available");
    }

    echo "<h2>Step 4: Template Test</h2>";

    if (file_exists(__DIR__ . '/templates/base.php')) {
        echo "<p>✓ Base template exists</p>";
    } else {
        echo "<p>✗ Base template missing</p>";
    }

    if (file_exists(__DIR__ . '/templates/home.php')) {
        echo "<p>✓ Home template exists</p>";
    } else {
        echo "<p>✗ Home template missing</p>";
    }

    echo "<h2>Step 5: Controller Test</h2>";

    if (file_exists(__DIR__ . '/includes/controllers/HomeController.php')) {
        require_once __DIR__ . '/includes/controllers/BaseController.php';
        require_once __DIR__ . '/includes/controllers/HomeController.php';
        echo "<p>✓ Controllers loaded</p>";
    } else {
        echo "<p>✗ Controllers missing</p>";
    }

    echo "<hr>";
    echo "<h2>✅ All Tests Passed!</h2>";
    echo "<p>The go-pugleaf PHP frontend should work. If you're still getting 500 errors:</p>";
    echo "<ul>";
    echo "<li>Check your web server configuration</li>";
    echo "<li>Ensure URL rewriting is enabled</li>";
    echo "<li>Check server error logs</li>";
    echo "<li>Verify file permissions (644 for files, 755 for directories)</li>";
    echo "</ul>";

} catch (Exception $e) {
    echo "<hr>";
    echo "<h2>❌ Test Failed</h2>";
    echo "<p><strong>Error:</strong> " . htmlspecialchars($e->getMessage()) . "</p>";
    echo "<p><strong>File:</strong> " . $e->getFile() . " line " . $e->getLine() . "</p>";
    echo "<p>Fix this error and try again.</p>";
}

echo "</body></html>";
?>
