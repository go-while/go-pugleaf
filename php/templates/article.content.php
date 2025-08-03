<?php
/**
 * Article view template
 */
$template = 'base';
?>

<div class="row">
    <div class="col-lg-8">
        <!-- Article Header -->
        <div class="card mb-4">
            <div class="card-header">
                <h1 class="card-title h3 mb-0"><?= h($article['subject'] ?? '[No Subject]') ?></h1>
            </div>
            <div class="card-body">
                <div class="row article-meta">
                    <div class="col-sm-6">
                        <strong>From:</strong> <?= h($article['from_header'] ?? 'Unknown') ?><br>
                        <strong>Date:</strong> <?= formatDate($article['date'] ?? $article['article_date']) ?>
                    </div>
                    <div class="col-sm-6">
                        <?php if (!empty($article['newsgroups'])): ?>
                        <strong>Newsgroups:</strong> <?= h($article['newsgroups']) ?><br>
                        <?php endif; ?>

                        <?php if (!empty($article['message_id'])): ?>
                        <strong>Message-ID:</strong>
                        <small class="text-muted"><?= h($article['message_id']) ?></small>
                        <?php endif; ?>
                    </div>
                </div>

                <?php if (!empty($article['references'])): ?>
                <div class="mt-2">
                    <small class="text-muted">
                        <strong>References:</strong> <?= h($article['references']) ?>
                    </small>
                </div>
                <?php endif; ?>
            </div>
        </div>

        <!-- Article Body -->
        <div class="card">
            <div class="card-body">
                <div class="article-body">
                    <?= sanitizeArticleBody($article['body'] ?? $article['body_text'] ?? '[No content]') ?>
                </div>
            </div>
        </div>

        <!-- Navigation -->
        <div class="d-flex justify-content-between mt-4">
            <div>
                <?php if (!empty($group_name)): ?>
                <a href="/groups/<?= urlencode($group_name) ?>" class="btn btn-outline-secondary">
                    ← Back to <?= h($group_name) ?>
                </a>
                <?php endif; ?>
            </div>
            <div>
                <!-- Add navigation buttons for prev/next articles if available -->
                <?php if (!empty($article['article_number']) && !empty($group_name)): ?>
                <div class="btn-group">
                    <a href="/groups/<?= urlencode($group_name) ?>/articles/<?= (int)$article['article_number'] - 1 ?>"
                       class="btn btn-outline-secondary">← Previous</a>
                    <a href="/groups/<?= urlencode($group_name) ?>/articles/<?= (int)$article['article_number'] + 1 ?>"
                       class="btn btn-outline-secondary">Next →</a>
                </div>
                <?php endif; ?>
            </div>
        </div>
    </div>

    <div class="col-lg-4">
        <!-- Article Info Sidebar -->
        <div class="card">
            <div class="card-header">
                <h5>Article Information</h5>
            </div>
            <div class="card-body">
                <?php if (!empty($article['article_number'])): ?>
                <p><strong>Article #:</strong> <?= number_format($article['article_number']) ?></p>
                <?php endif; ?>

                <?php if (!empty($article['lines'])): ?>
                <p><strong>Lines:</strong> <?= number_format($article['lines']) ?></p>
                <?php endif; ?>

                <?php if (!empty($article['bytes'])): ?>
                <p><strong>Size:</strong> <?= formatBytes($article['bytes']) ?></p>
                <?php endif; ?>

                <?php if (!empty($article['xref'])): ?>
                <p><strong>Xref:</strong> <small><?= h($article['xref']) ?></small></p>
                <?php endif; ?>
            </div>
        </div>

        <!-- Thread Navigation -->
        <?php if (!empty($article['references']) || !empty($article['message_id'])): ?>
        <div class="card mt-3">
            <div class="card-header">
                <h6>Thread Navigation</h6>
            </div>
            <div class="card-body">
                <?php if (!empty($group_name)): ?>
                <a href="/groups/<?= urlencode($group_name) ?>/threads" class="btn btn-sm btn-outline-primary">
                    View All Threads
                </a>
                <?php endif; ?>

                <!-- Add thread-specific navigation here -->
                <p class="mt-2 mb-0"><small class="text-muted">Thread navigation will be enhanced in future updates.</small></p>
            </div>
        </div>
        <?php endif; ?>

        <!-- Actions -->
        <div class="card mt-3">
            <div class="card-header">
                <h6>Actions</h6>
            </div>
            <div class="card-body">
                <div class="d-grid gap-2">
                    <?php if (!empty($article['message_id'])): ?>
                    <a href="/articles/<?= urlencode($article['message_id']) ?>" class="btn btn-sm btn-outline-secondary">
                        View by Message-ID
                    </a>
                    <?php endif; ?>

                    <!-- Add more actions like Reply, Forward, etc. -->
                    <small class="text-muted">More actions coming soon...</small>
                </div>
            </div>
        </div>
    </div>
</div>
