<?php
/**
 * Debug API Test - Test SSL connection issues
 */

// Include the configuration
if (file_exists(__DIR__ . '/config.inc.php')) {
    require_once __DIR__ . '/config.inc.php';
} else {
    die('Error: config.inc.php not found.');
}

header('Content-Type: text/html; charset=utf-8');

echo "<h1>SSL/API Debug Test</h1>";

$url = PUGLEAF_API_BASE . '/groups?page=1&page_size=5';
echo "<p><strong>Testing URL:</strong> " . htmlspecialchars($url) . "</p>";

// Test 1: Basic cURL connection
echo "<h2>Test 1: Basic cURL Connection</h2>";

$ch = curl_init();
curl_setopt_array($ch, [
    CURLOPT_URL => $url,
    CURLOPT_RETURNTRANSFER => true,
    CURLOPT_TIMEOUT => 30,
    CURLOPT_VERBOSE => true,
    CURLOPT_HTTPHEADER => [
        'Content-Type: application/json',
        'Accept: application/json',
        'User-Agent: PugleafDebug/1.0'
    ],
    CURLOPT_SSL_VERIFYPEER => true,
    CURLOPT_SSL_VERIFYHOST => 2,
]);

$response = curl_exec($ch);
$httpCode = curl_getinfo($ch, CURLINFO_HTTP_CODE);
$curlError = curl_error($ch);
$curlInfo = curl_getinfo($ch);
curl_close($ch);

echo "<p><strong>HTTP Code:</strong> $httpCode</p>";
echo "<p><strong>cURL Error:</strong> " . ($curlError ? htmlspecialchars($curlError) : 'None') . "</p>";

if ($response) {
    echo "<p><strong>Response Length:</strong> " . strlen($response) . " bytes</p>";
    echo "<p><strong>Response Preview:</strong></p>";
    echo "<pre>" . htmlspecialchars(substr($response, 0, 500)) . "</pre>";
} else {
    echo "<p><strong>No response received</strong></p>";
}

// Test 2: With SSL verification disabled (for debugging only)
if ($curlError && strpos($curlError, 'SSL') !== false) {
    echo "<h2>Test 2: SSL Verification Disabled (Debug Only)</h2>";
    echo "<p style='color: red;'><strong>WARNING:</strong> This test disables SSL verification for debugging purposes only!</p>";

    $ch = curl_init();
    curl_setopt_array($ch, [
        CURLOPT_URL => $url,
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_TIMEOUT => 30,
        CURLOPT_HTTPHEADER => [
            'Content-Type: application/json',
            'Accept: application/json',
            'User-Agent: PugleafDebug/1.0'
        ],
        CURLOPT_SSL_VERIFYPEER => false,
        CURLOPT_SSL_VERIFYHOST => false,
    ]);

    $response2 = curl_exec($ch);
    $httpCode2 = curl_getinfo($ch, CURLINFO_HTTP_CODE);
    $curlError2 = curl_error($ch);
    curl_close($ch);

    echo "<p><strong>HTTP Code:</strong> $httpCode2</p>";
    echo "<p><strong>cURL Error:</strong> " . ($curlError2 ? htmlspecialchars($curlError2) : 'None') . "</p>";

    if ($response2) {
        echo "<p><strong>Response Length:</strong> " . strlen($response2) . " bytes</p>";
        echo "<p><strong>Response Preview:</strong></p>";
        echo "<pre>" . htmlspecialchars(substr($response2, 0, 500)) . "</pre>";

        // Try to decode JSON
        $decoded = json_decode($response2, true);
        if ($decoded) {
            echo "<p><strong>JSON Decoded Successfully:</strong></p>";
            echo "<pre>" . htmlspecialchars(print_r($decoded, true)) . "</pre>";
        } else {
            echo "<p><strong>JSON Decode Error:</strong> " . json_last_error_msg() . "</p>";
        }
    } else {
        echo "<p><strong>No response received</strong></p>";
    }
}

// Test 3: Connection info
echo "<h2>Connection Information</h2>";
echo "<pre>";
foreach ($curlInfo as $key => $value) {
    if (is_string($value) || is_numeric($value)) {
        echo htmlspecialchars($key) . ": " . htmlspecialchars($value) . "\n";
    }
}
echo "</pre>";

// Test 4: PHP environment
echo "<h2>PHP Environment</h2>";
echo "<p><strong>PHP Version:</strong> " . phpversion() . "</p>";
echo "<p><strong>cURL Version:</strong> " . (function_exists('curl_version') ? print_r(curl_version(), true) : 'Not available') . "</p>";
echo "<p><strong>OpenSSL Version:</strong> " . (defined('OPENSSL_VERSION_TEXT') ? OPENSSL_VERSION_TEXT : 'Not available') . "</p>";

echo "<hr>";
echo "<p><small>Debug test completed at " . date('Y-m-d H:i:s') . "</small></p>";
?>
