<?php
/**
 * Stats page template
 */
$template = 'base';
?>

<div class="row">
    <div class="col-lg-8">
        <h1>System Statistics</h1>

        <?php if (!empty($stats)): ?>

        <!-- Overview Cards -->
        <div class="row mb-4">
            <div class="col-md-3 mb-3">
                <div class="card stats-card text-center">
                    <div class="card-body">
                        <h3 class="text-primary"><?= number_format($stats['total_groups'] ?? 0) ?></h3>
                        <small>Total Groups</small>
                    </div>
                </div>
            </div>

            <div class="col-md-3 mb-3">
                <div class="card stats-card text-center">
                    <div class="card-body">
                        <h3 class="text-success"><?= number_format($stats['total_articles'] ?? 0) ?></h3>
                        <small>Total Articles</small>
                    </div>
                </div>
            </div>

            <div class="col-md-3 mb-3">
                <div class="card stats-card text-center">
                    <div class="card-body">
                        <h3 class="text-info"><?= number_format($stats['total_threads'] ?? 0) ?></h3>
                        <small>Total Threads</small>
                    </div>
                </div>
            </div>

            <div class="col-md-3 mb-3">
                <div class="card stats-card text-center">
                    <div class="card-body">
                        <h3 class="text-warning"><?= formatBytes($stats['total_size'] ?? 0) ?></h3>
                        <small>Total Size</small>
                    </div>
                </div>
            </div>
        </div>

        <!-- Detailed Stats -->
        <div class="card mb-4">
            <div class="card-header">
                <h5>Database Statistics</h5>
            </div>
            <div class="card-body">
                <div class="table-responsive">
                    <table class="table">
                        <tbody>
                            <tr>
                                <td><strong>Active Groups</strong></td>
                                <td><?= number_format($stats['active_groups'] ?? 0) ?></td>
                            </tr>
                            <tr>
                                <td><strong>Inactive Groups</strong></td>
                                <td><?= number_format(($stats['total_groups'] ?? 0) - ($stats['active_groups'] ?? 0)) ?></td>
                            </tr>
                            <tr>
                                <td><strong>Average Articles per Group</strong></td>
                                <td>
                                    <?php if (($stats['active_groups'] ?? 0) > 0): ?>
                                        <?= number_format(($stats['total_articles'] ?? 0) / $stats['active_groups']) ?>
                                    <?php else: ?>
                                        0
                                    <?php endif; ?>
                                </td>
                            </tr>
                            <?php if (isset($stats['oldest_article'])): ?>
                            <tr>
                                <td><strong>Oldest Article</strong></td>
                                <td><?= formatDate($stats['oldest_article']) ?></td>
                            </tr>
                            <?php endif; ?>

                            <?php if (isset($stats['newest_article'])): ?>
                            <tr>
                                <td><strong>Newest Article</strong></td>
                                <td><?= formatDate($stats['newest_article']) ?></td>
                            </tr>
                            <?php endif; ?>
                        </tbody>
                    </table>
                </div>
            </div>
        </div>

        <!-- Top Groups -->
        <?php if (!empty($stats['top_groups'])): ?>
        <div class="card">
            <div class="card-header">
                <h5>Most Active Groups</h5>
            </div>
            <div class="card-body">
                <div class="table-responsive">
                    <table class="table">
                        <thead>
                            <tr>
                                <th>Group Name</th>
                                <th class="text-end">Articles</th>
                                <th class="text-end">Size</th>
                            </tr>
                        </thead>
                        <tbody>
                            <?php foreach ($stats['top_groups'] as $group): ?>
                            <tr>
                                <td>
                                    <a href="/groups/<?= urlencode($group['name']) ?>" class="text-decoration-none">
                                        <?= h($group['name']) ?>
                                    </a>
                                </td>
                                <td class="text-end"><?= number_format($group['article_count'] ?? 0) ?></td>
                                <td class="text-end"><?= formatBytes($group['total_size'] ?? 0) ?></td>
                            </tr>
                            <?php endforeach; ?>
                        </tbody>
                    </table>
                </div>
            </div>
        </div>
        <?php endif; ?>

        <?php else: ?>
        <div class="alert alert-info">
            <h4>Statistics Unavailable</h4>
            <p>Statistics are not available at the moment. The backend may still be processing data.</p>
        </div>
        <?php endif; ?>
    </div>

    <div class="col-lg-4">
        <div class="card">
            <div class="card-header">
                <h5>System Information</h5>
            </div>
            <div class="card-body">
                <?php if (isset($stats['last_update'])): ?>
                <p><strong>Last Update:</strong><br>
                <?= formatRelativeTime($stats['last_update']) ?></p>
                <?php endif; ?>

                <?php if (isset($stats['backend_version'])): ?>
                <p><strong>Backend Version:</strong><br>
                <?= h($stats['backend_version']) ?></p>
                <?php endif; ?>

                <?php if (isset($stats['uptime'])): ?>
                <p><strong>Uptime:</strong><br>
                <?= h($stats['uptime']) ?></p>
                <?php endif; ?>

                <p><strong>Frontend:</strong><br>
                go-pugleaf PHP Frontend</p>
            </div>
        </div>

        <?php if (!empty($stats['providers'])): ?>
        <div class="card mt-3">
            <div class="card-header">
                <h6>NNTP Providers</h6>
            </div>
            <div class="card-body">
                <?php foreach ($stats['providers'] as $provider): ?>
                <div class="mb-2">
                    <strong><?= h($provider['name']) ?></strong><br>
                    <small class="text-muted">
                        <?= h($provider['server']) ?>:<?= (int)($provider['port'] ?? 119) ?>
                        <?php if (!empty($provider['ssl'])): ?>
                        <span class="badge bg-success">SSL</span>
                        <?php endif; ?>
                    </small>
                </div>
                <?php endforeach; ?>
            </div>
        </div>
        <?php endif; ?>

        <div class="card mt-3">
            <div class="card-header">
                <h6>Actions</h6>
            </div>
            <div class="card-body">
                <div class="d-grid gap-2">
                    <a href="/" class="btn btn-outline-primary">View Sections</a>
                    <a href="/groups" class="btn btn-outline-secondary">Browse Groups</a>
                    <a href="/search" class="btn btn-outline-info">Search Articles</a>
                </div>
            </div>
        </div>
    </div>
</div>
