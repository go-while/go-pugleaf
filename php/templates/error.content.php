<?php
/**
 * Error page template
 */
$template = 'base';
?>

<div class="row justify-content-center">
    <div class="col-lg-6">
        <div class="text-center">
            <h1 class="display-1 text-muted"><?= (int)($error_code ?? 500) ?></h1>
            <h2><?= h($title ?? 'Error') ?></h2>
            <p class="lead"><?= h($message ?? 'An unexpected error occurred.') ?></p>

            <div class="mt-4">
                <a href="/" class="btn btn-primary">Go Home</a>
                <a href="javascript:history.back()" class="btn btn-outline-secondary">Go Back</a>
            </div>
        </div>
    </div>
</div>
