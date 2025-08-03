<?php
/**
 * Debug API Test - Test the specific groups API call
 */

// Include the configuration
if (file_exists(__DIR__ . '/config.inc.php')) {
    require_once __DIR__ . '/config.inc.php';
} else {
    die('Error: config.inc.php not found.');
}

// Include the API client
require_once __DIR__ . '/includes/api_client.php';

header('Content-Type: text/html; charset=utf-8');

echo "<h1>API Debug Test - Groups API Call</h1>";

try {
    $api = new PugleafApiClient(PUGLEAF_API_BASE);

    echo "<p><strong>API Base URL:</strong> " . htmlspecialchars(PUGLEAF_API_BASE) . "</p>";

    echo "<h2>Testing getNewsgroups() method</h2>";

    $result = $api->getNewsgroups(1, 5);

    echo "<p><strong>SUCCESS!</strong> API call completed successfully.</p>";
    echo "<p><strong>Result type:</strong> " . gettype($result) . "</p>";
    echo "<p><strong>Result data:</strong></p>";
    echo "<pre>" . htmlspecialchars(json_encode($result, JSON_PRETTY_PRINT)) . "</pre>";

} catch (Exception $e) {
    echo "<p><strong>ERROR:</strong> " . htmlspecialchars($e->getMessage()) . "</p>";
    echo "<p><strong>Error in file:</strong> " . htmlspecialchars($e->getFile()) . ":" . $e->getLine() . "</p>";
    echo "<p><strong>Stack trace:</strong></p>";
    echo "<pre>" . htmlspecialchars($e->getTraceAsString()) . "</pre>";
}

echo "<h2>Raw cURL Test (Same as SSL Debug)</h2>";

$url = PUGLEAF_API_BASE . '/groups?page=1&page_size=5';

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
    CURLOPT_SSL_VERIFYPEER => true,
    CURLOPT_SSL_VERIFYHOST => 2,
]);

$response = curl_exec($ch);
$httpCode = curl_getinfo($ch, CURLINFO_HTTP_CODE);
$curlError = curl_error($ch);
curl_close($ch);

echo "<p><strong>Direct cURL HTTP Code:</strong> $httpCode</p>";
echo "<p><strong>Direct cURL Error:</strong> " . ($curlError ? htmlspecialchars($curlError) : 'None') . "</p>";

if ($response) {
    echo "<p><strong>Direct cURL Response Length:</strong> " . strlen($response) . " bytes</p>";
    echo "<p><strong>Direct cURL Response Preview:</strong></p>";
    echo "<pre>" . htmlspecialchars(substr($response, 0, 300)) . "</pre>";
} else {
    echo "<p><strong>No response from direct cURL</strong></p>";
}

?>
