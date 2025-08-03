<?php
/**
 * Debug Controller Test - Test GroupsController instantiation
 */

// Include the configuration
if (file_exists(__DIR__ . '/config.inc.php')) {
    require_once __DIR__ . '/config.inc.php';
} else {
    die('Error: config.inc.php not found.');
}

// Include dependencies
require_once __DIR__ . '/includes/api_client.php';
require_once __DIR__ . '/includes/template_engine.php';
require_once __DIR__ . '/includes/utils.php';
require_once __DIR__ . '/includes/controllers/BaseController.php';
require_once __DIR__ . '/includes/controllers/GroupsController.php';

header('Content-Type: text/html; charset=utf-8');

echo "<h1>Controller Debug Test</h1>";

try {
    echo "<h2>Step 1: Create API Client</h2>";
    $api = new PugleafApiClient(PUGLEAF_API_BASE);
    echo "<p>✓ API client created successfully</p>";

    echo "<h2>Step 2: Create Template Engine</h2>";
    $template = new TemplateEngine(__DIR__ . '/templates');
    echo "<p>✓ Template engine created successfully</p>";

    echo "<h2>Step 3: Create GroupsController</h2>";
    $controller = new GroupsController($api, $template);
    echo "<p>✓ GroupsController created successfully</p>";

    echo "<h2>Step 4: Test getNewsgroups API call directly</h2>";
    $result = $api->getNewsgroups(1, 5);
    echo "<p>✓ API call successful - got " . count($result['data']) . " groups</p>";

    echo "<h2>Step 5: Call controller index method</h2>";
    // Simulate the parameters that would come from the router
    $params = [];
    $_GET['page'] = 1;
    $_GET['page_size'] = 5;

    // Capture output
    ob_start();
    $controller->index($params);
    $output = ob_get_clean();

    echo "<p>✓ Controller index method executed successfully</p>";
    echo "<p><strong>Output length:</strong> " . strlen($output) . " bytes</p>";

    if (strlen($output) > 0) {
        echo "<p><strong>Output preview (first 500 chars):</strong></p>";
        echo "<pre>" . htmlspecialchars(substr($output, 0, 500)) . "</pre>";
    }

} catch (Exception $e) {
    echo "<p><strong>ERROR:</strong> " . htmlspecialchars($e->getMessage()) . "</p>";
    echo "<p><strong>Error in file:</strong> " . htmlspecialchars($e->getFile()) . ":" . $e->getLine() . "</p>";
    echo "<p><strong>Stack trace:</strong></p>";
    echo "<pre>" . htmlspecialchars($e->getTraceAsString()) . "</pre>";
}

?>
