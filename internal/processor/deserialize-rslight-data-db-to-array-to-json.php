<?php
/**
 * RSLight Data Database to JSON Converter
 *
 * This tool converts RSLight SQLite database files (*-data.db3) to JSON format,
 * deserializing PHP-serialized header data from the threads table.
 *
 * Usage: php deserialize-rslight-data-db-to-array-to-json.php <input-file.db3>
 * Output: Creates <input-file>.json in the same directory
 */

// Check if running from command line
if (php_sapi_name() !== 'cli') {
    die("This script must be run from the command line.\n");
}

// Check command line arguments
if ($argc !== 2) {
    echo "Usage: php " . basename($argv[0]) . " <input-database.db3>\n";
    echo "Example: php " . basename($argv[0]) . " comp.programming-data.db3\n";
    exit(1);
}

$inputFile = $argv[1];

// Validate input file
if (!file_exists($inputFile)) {
    die("Error: Input file '{$inputFile}' does not exist.\n");
}

if (!is_readable($inputFile)) {
    die("Error: Input file '{$inputFile}' is not readable.\n");
}

// Generate output filename
$outputFile = preg_replace('/\.db3$/', '.json', $inputFile);
if ($outputFile === $inputFile) {
    $outputFile = $inputFile . '.json';
}

echo "Converting RSLight database: {$inputFile}\n";
echo "Output will be written to: {$outputFile}\n";

try {
    // Open SQLite database
    $pdo = new PDO("sqlite:{$inputFile}");
    $pdo->setAttribute(PDO::ATTR_ERRMODE, PDO::ERRMODE_EXCEPTION);

    echo "Successfully opened database.\n";

    // Get database schema info
    $tablesStmt = $pdo->query("SELECT name FROM sqlite_master WHERE type='table'");
    $tables = $tablesStmt->fetchAll(PDO::FETCH_COLUMN);
    echo "Available tables: " . implode(', ', $tables) . "\n";

    // Check if threads table exists
    if (!in_array('threads', $tables)) {
        die("Error: 'threads' table not found in database.\n");
    }

    // Get table schema for threads
    $schemaStmt = $pdo->query("PRAGMA table_info(threads)");
    $schema = $schemaStmt->fetchAll(PDO::FETCH_ASSOC);
    echo "Threads table schema:\n";
    foreach ($schema as $column) {
        echo "  - {$column['name']}: {$column['type']}\n";
    }

    // Get row count
    $countStmt = $pdo->query("SELECT COUNT(*) FROM threads");
    $totalRows = $countStmt->fetchColumn();
    echo "Total rows in threads table: {$totalRows}\n";

    if ($totalRows == 0) {
        echo "Warning: No data found in threads table.\n";
    }

    // Prepare the main query
    $stmt = $pdo->prepare("SELECT * FROM threads ORDER BY id");
    $stmt->execute();

    $results = [];
    $processedCount = 0;
    $errorCount = 0;

    echo "Processing rows...\n";

    while ($row = $stmt->fetch(PDO::FETCH_ASSOC)) {
        $processedCount++;

        // Show progress every 100 rows
        if ($processedCount % 100 == 0) {
            echo "Processed {$processedCount}/{$totalRows} rows...\n";
        }

        $processedRow = [
            'id' => $row['id']
        ];

        // Process each column
        foreach ($row as $column => $value) {
            if ($column === 'id') {
                continue; // Already added
            }

            if ($column === 'headers' && !empty($value)) {
                // Try to deserialize PHP-serialized data
                $unserialized = @unserialize($value);
                if ($unserialized !== false) {
                    $processedRow['headers_deserialized'] = $unserialized;
                    $processedRow['headers_raw'] = $value; // Keep original for reference
                } else {
                    // If unserialization fails, keep as raw data
                    $processedRow['headers_raw'] = $value;
                    $processedRow['headers_deserialization_error'] = 'Failed to unserialize';
                    $errorCount++;
                }
            } else {
                // For all other columns, keep as-is
                $processedRow[$column] = $value;
            }
        }

        $results[] = $processedRow;
    }

    echo "Finished processing {$processedCount} rows.\n";
    if ($errorCount > 0) {
        echo "Warning: {$errorCount} rows had deserialization errors.\n";
    }

    // Create output data structure
    $output = [
        'metadata' => [
            'source_file' => basename($inputFile),
            'conversion_time' => date('Y-m-d H:i:s'),
            'total_rows' => $totalRows,
            'processed_rows' => $processedCount,
            'deserialization_errors' => $errorCount,
            'tables_in_source' => $tables,
            'threads_table_schema' => $schema
        ],
        'threads' => $results
    ];

    // Write JSON output
    echo "Writing JSON output to {$outputFile}...\n";
    $jsonFlags = JSON_PRETTY_PRINT | JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES;
    $jsonData = json_encode($output, $jsonFlags);

    if ($jsonData === false) {
        die("Error: Failed to encode data as JSON: " . json_last_error_msg() . "\n");
    }

    if (file_put_contents($outputFile, $jsonData) === false) {
        die("Error: Failed to write output file '{$outputFile}'.\n");
    }

    $fileSize = filesize($outputFile);
    echo "Successfully created {$outputFile} ({$fileSize} bytes)\n";

    // Show sample of deserialized data if available
    if (!empty($results)) {
        echo "\nSample of first record:\n";
        $sample = $results[0];
        if (isset($sample['headers_deserialized'])) {
            echo "Headers (deserialized): " . json_encode($sample['headers_deserialized'], JSON_PRETTY_PRINT) . "\n";
        }
        echo "Full first record structure: " . json_encode($sample, JSON_PRETTY_PRINT | JSON_PARTIAL_OUTPUT_ON_ERROR) . "\n";
    }

    echo "\nConversion completed successfully!\n";

} catch (PDOException $e) {
    die("Database error: " . $e->getMessage() . "\n");
} catch (Exception $e) {
    die("Error: " . $e->getMessage() . "\n");
}
?>
