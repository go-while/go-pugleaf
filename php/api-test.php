<?php
/**
 * Simple API Test - Check if the go-pugleaf backend is accessible
 * Place this in your web root and access it via browser to test connectivity
 */

// Include the configuration
if (file_exists(__DIR__ . '/config.inc.php')) {
    require_once __DIR__ . '/config.inc.php';
} else {
    die('Error: config.inc.php not found. Please copy config.sample.php to config.inc.php and configure it.');
}

// Include the API client
require_once __DIR__ . '/includes/api_client.php';

// Set content type
header('Content-Type: text/html; charset=utf-8');

echo "<h1>go-pugleaf PHP Frontend API Test</h1>";

// Test configuration
echo "<h2>Configuration</h2>";
echo "<p><strong>API Base URL:</strong> " . htmlspecialchars(PUGLEAF_API_BASE) . "</p>";
echo "<p><strong>Web Base URL:</strong> " . htmlspecialchars(PUGLEAF_WEB_BASE) . "</p>";
echo "<p><strong>Debug Mode:</strong> " . (DEBUG_MODE ? 'Enabled' : 'Disabled') . "</p>";

// Test API connection
echo "<h2>API Connection Test</h2>";

try {
    $api = new PugleafApiClient(PUGLEAF_API_BASE);

    // Test 1: Health check (if available)
    echo "<p><strong>Health Check:</strong> ";
    if ($api->healthCheck()) {
        echo "<span style='color: green;'>✓ OK</span></p>";
    } else {
        echo "<span style='color: orange;'>⚠ No health endpoint or failed</span></p>";
    }

    // Test 2: Get newsgroups
    echo "<p><strong>Groups API Test:</strong> ";
    $result = $api->getNewsgroups(1, 5);

    if (isset($result['data']) && is_array($result['data'])) {
        $count = count($result['data']);
        $total = $result['total_count'] ?? 'unknown';
        echo "<span style='color: green;'>✓ OK</span> - Retrieved {$count} groups (total: {$total})</p>";

        if ($count > 0) {
            echo "<h3>Sample Groups:</h3>";
            echo "<ul>";
            foreach (array_slice($result['data'], 0, 3) as $group) {
                $name = htmlspecialchars($group['name'] ?? 'Unknown');
                $articleCount = $group['high'] - $group['low'] + 1;
                echo "<li>{$name} ({$articleCount} articles)</li>";
            }
            echo "</ul>";
        }

        // Test 3: Get first group overview if available
        if ($count > 0) {
            $firstGroup = $result['data'][0];
            $groupName = $firstGroup['name'];

            echo "<p><strong>Group Overview Test ({$groupName}):</strong> ";
            try {
                $overview = $api->getGroupOverview($groupName, 1, 3);

                if (isset($overview['data']) && is_array($overview['data'])) {
                    $articleCount = count($overview['data']);
                    echo "<span style='color: green;'>✓ OK</span> - Retrieved {$articleCount} articles</p>";

                    if ($articleCount > 0) {
                        echo "<h3>Sample Articles from {$groupName}:</h3>";
                        echo "<ul>";
                        foreach ($overview['data'] as $article) {
                            $subject = htmlspecialchars($article['subject'] ?? 'No subject');
                            $from = htmlspecialchars($article['from'] ?? 'Unknown');
                            $articleNum = $article['article_num'] ?? 'N/A';
                            echo "<li>#{$articleNum}: {$subject} (by {$from})</li>";
                        }
                        echo "</ul>";
                    }
                } else {
                    echo "<span style='color: orange;'>⚠ No articles returned</span></p>";
                }
            } catch (Exception $e) {
                echo "<span style='color: red;'>✗ FAILED</span> - " . htmlspecialchars($e->getMessage()) . "</p>";
            }
        }

    } else {
        echo "<span style='color: red;'>✗ FAILED</span> - Invalid response format</p>";
        echo "<pre>Response: " . htmlspecialchars(print_r($result, true)) . "</pre>";
    }

} catch (Exception $e) {
    echo "<span style='color: red;'>✗ FAILED</span> - " . htmlspecialchars($e->getMessage()) . "</p>";

    // Additional debugging info
    echo "<h3>Debug Information:</h3>";
    echo "<p><strong>Full URL being accessed:</strong> " . htmlspecialchars(PUGLEAF_API_BASE . '/groups?page=1&page_size=5') . "</p>";

    // Check if it's a connection issue
    if (strpos($e->getMessage(), 'Connection error') !== false) {
        echo "<p><strong>Possible issues:</strong></p>";
        echo "<ul>";
        echo "<li>go-pugleaf backend is not running</li>";
        echo "<li>Backend is running on a different port</li>";
        echo "<li>Firewall blocking the connection</li>";
        echo "<li>Incorrect API_BASE_URL configuration</li>";
        echo "</ul>";
    }
}

echo "<h2>PHP Information</h2>";
echo "<p><strong>PHP Version:</strong> " . phpversion() . "</p>";
echo "<p><strong>cURL Available:</strong> " . (function_exists('curl_init') ? 'Yes' : 'No') . "</p>";
echo "<p><strong>JSON Available:</strong> " . (function_exists('json_decode') ? 'Yes' : 'No') . "</p>";

echo "<hr>";
echo "<p><small>Test completed at " . date('Y-m-d H:i:s') . "</small></p>";
?>
