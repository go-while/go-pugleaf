<?php
/**
 * Home page template
 * Converted from Go template
 */
$template = 'base';
?>

<div class="row">
    <div class="col-lg-8">
        <h1>Welcome to <?= h($site_name ?? 'PugLeaf') ?></h1>
        <p class="lead">A modern NNTP newsgroup reader</p>

        <?php if (!empty($sections)): ?>
        <h2>Sections</h2>
        <div class="row">
            <?php foreach ($sections as $section): ?>
            <div class="col-md-6 col-lg-4 mb-3">
                <div class="card">
                    <div class="card-body">
                        <h5 class="card-title">
                            <a href="/sections/<?= urlencode($section['name']) ?>" class="text-decoration-none">
                                <?= h($section['display_name'] ?? $section['name']) ?>
                            </a>
                        </h5>

                        <?php if (!empty($section['description'])): ?>
                        <p class="card-text"><?= h($section['description']) ?></p>
                        <?php endif; ?>

                        <small class="text-muted">
                            <?= (int)($section['group_count'] ?? 0) ?> groups
                        </small>
                    </div>
                </div>
            </div>
            <?php endforeach; ?>
        </div>
        <?php else: ?>
        <div class="alert alert-info">
            <h4>Getting Started</h4>
            <p>No sections found. The NNTP backend may still be fetching data.</p>
            <a href="/groups" class="btn btn-primary">View All Groups</a>
        </div>
        <?php endif; ?>
    </div>

    <div class="col-lg-4">
        <h3>Quick Stats</h3>

        <?php if (!empty($stats)): ?>
        <div class="row">
            <div class="col-sm-6 col-lg-12 mb-3">
                <div class="card stats-card">
                    <div class="card-body text-center">
                        <h4 class="text-primary"><?= number_format($stats['total_groups'] ?? 0) ?></h4>
                        <small>Total Groups</small>
                    </div>
                </div>
            </div>

            <div class="col-sm-6 col-lg-12 mb-3">
                <div class="card stats-card">
                    <div class="card-body text-center">
                        <h4 class="text-success"><?= number_format($stats['total_articles'] ?? 0) ?></h4>
                        <small>Total Articles</small>
                    </div>
                </div>
            </div>

            <?php if (isset($stats['last_update'])): ?>
            <div class="col-sm-12 mb-3">
                <div class="card">
                    <div class="card-body">
                        <small class="text-muted">
                            <strong>Last Update:</strong><br>
                            <?= formatRelativeTime($stats['last_update']) ?>
                        </small>
                    </div>
                </div>
            </div>
            <?php endif; ?>
        </div>
        <?php endif; ?>

        <div class="card">
            <div class="card-header">
                <h5>Navigation</h5>
            </div>
            <div class="card-body">
                <div class="list-group list-group-flush">
                    <a href="/groups" class="list-group-item list-group-item-action">
                        Browse All Groups
                    </a>
                    <a href="/search" class="list-group-item list-group-item-action">
                        Search Articles
                    </a>
                    <a href="/stats" class="list-group-item list-group-item-action">
                        View Statistics
                    </a>
                </div>
            </div>
        </div>
    </div>
</div>
