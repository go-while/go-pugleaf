<?php
/**
 * Template Debug Test
 */

// Include the configuration
if (file_exists(__DIR__ . '/config.inc.php')) {
    require_once __DIR__ . '/config.inc.php';
} else {
    die('Error: config.inc.php not found.');
}

// Error reporting for debugging
if (defined('DEBUG_MODE') && DEBUG_MODE) {
    error_reporting(E_ALL);
    ini_set('display_errors', 1);
}

// Include required files
require_once __DIR__ . '/includes/template_engine.php';
require_once __DIR__ . '/includes/utils.php';

header('Content-Type: text/html; charset=utf-8');

echo "<h1>Template Debug Test</h1>";

// Test 1: Check if templates directory exists
$templateDir = __DIR__ . '/templates';
echo "<h2>Template Directory Check</h2>";
echo "<p><strong>Template directory:</strong> $templateDir</p>";

if (is_dir($templateDir)) {
    echo "<p>✓ Template directory exists</p>";
    
    // List templates
    $templates = glob($templateDir . '/*.php');
    echo "<p><strong>Available templates:</strong></p>";
    echo "<ul>";
    foreach ($templates as $template) {
        $name = basename($template);
        $readable = is_readable($template) ? '✓' : '✗';
        echo "<li>$readable $name</li>";
    }
    echo "</ul>";
} else {
    echo "<p>✗ Template directory does not exist</p>";
}

// Test 2: Initialize template engine
echo "<h2>Template Engine Test</h2>";
try {
    $template = new TemplateEngine($templateDir);
    echo "<p>✓ Template engine initialized successfully</p>";
    
    // Test 3: Try to render a simple template
    echo "<h2>Template Rendering Test</h2>";
    
    // Test base template existence
    $baseTemplate = $templateDir . '/base.php';
    if (file_exists($baseTemplate)) {
        echo "<p>✓ Base template exists</p>";
        
        // Check for syntax errors
        $syntaxCheck = shell_exec("php -l '$baseTemplate' 2>&1");
        if (strpos($syntaxCheck, 'No syntax errors') !== false) {
            echo "<p>✓ Base template has no syntax errors</p>";
        } else {
            echo "<p>✗ Base template has syntax errors:</p>";
            echo "<pre>" . htmlspecialchars($syntaxCheck) . "</pre>";
        }
    } else {
        echo "<p>✗ Base template does not exist</p>";
    }
    
    // Test home template
    $homeTemplate = $templateDir . '/home.php';
    if (file_exists($homeTemplate)) {
        echo "<p>✓ Home template exists</p>";
        
        $syntaxCheck = shell_exec("php -l '$homeTemplate' 2>&1");
        if (strpos($syntaxCheck, 'No syntax errors') !== false) {
            echo "<p>✓ Home template has no syntax errors</p>";
        } else {
            echo "<p>✗ Home template has syntax errors:</p>";
            echo "<pre>" . htmlspecialchars($syntaxCheck) . "</pre>";
        }
    } else {
        echo "<p>✗ Home template does not exist</p>";
    }
    
    // Test 4: Try a simple render
    echo "<h2>Simple Render Test</h2>";
    try {
        echo "<p>Attempting to render error template...</p>";
        echo "<div style='border: 1px solid #ccc; padding: 10px; margin: 10px 0;'>";
        
        $template->render('error', [
            'title' => 'Test Error',
            'message' => 'This is a test error message',
            'error_code' => 500
        ]);
        
        echo "</div>";
        echo "<p>✓ Error template rendered successfully</p>";
        
    } catch (Exception $e) {
        echo "<p>✗ Failed to render error template: " . htmlspecialchars($e->getMessage()) . "</p>";
        echo "<p><strong>Error details:</strong></p>";
        echo "<pre>" . htmlspecialchars($e->getTraceAsString()) . "</pre>";
    }
    
} catch (Exception $e) {
    echo "<p>✗ Failed to initialize template engine: " . htmlspecialchars($e->getMessage()) . "</p>";
}

// Test 5: Check helper functions
echo "<h2>Helper Functions Test</h2>";

if (function_exists('h')) {
    echo "<p>✓ h() function exists</p>";
    $test_string = '<script>alert("test")</script>';
    $escaped = h($test_string);
    echo "<p><strong>Test:</strong> h('$test_string') = " . htmlspecialchars($escaped) . "</p>";
} else {
    echo "<p>✗ h() function not found</p>";
}

if (function_exists('getBaseUrl')) {
    echo "<p>✓ getBaseUrl() function exists</p>";
    try {
        $baseUrl = getBaseUrl();
        echo "<p><strong>Base URL:</strong> " . htmlspecialchars($baseUrl) . "</p>";
    } catch (Exception $e) {
        echo "<p>✗ getBaseUrl() failed: " . htmlspecialchars($e->getMessage()) . "</p>";
    }
} else {
    echo "<p>✗ getBaseUrl() function not found</p>";
}

echo "<hr>";
echo "<p><small>Template debug completed at " . date('Y-m-d H:i:s') . "</small></p>";
?>
