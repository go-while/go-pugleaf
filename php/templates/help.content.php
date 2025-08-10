<?php
/**
 * Help page template
 */
$template = 'base';
?>

<div class="row">
    <div class="col-lg-8">
        <h1>Help & Documentation</h1>

        <div class="card mb-4">
            <div class="card-header">
                <h5>Getting Started</h5>
            </div>
            <div class="card-body">
                <p>Welcome to PugLeaf, a modern NNTP newsgroup reader powered by go-pugleaf backend.</p>

                <h6>Basic Navigation:</h6>
                <ul>
                    <li><strong>Home:</strong> Browse sections and get an overview</li>
                    <li><strong>Groups:</strong> View all available newsgroups</li>
                    <li><strong>Search:</strong> Find specific articles across all groups</li>
                    <li><strong>Stats:</strong> View system statistics and information</li>
                </ul>
            </div>
        </div>

        <div class="card mb-4">
            <div class="card-header">
                <h5>Reading Articles</h5>
            </div>
            <div class="card-body">
                <h6>Browsing Articles:</h6>
                <ul>
                    <li>Click on any group name to view its articles</li>
                    <li>Articles are listed with subject, author, and date</li>
                    <li>Use pagination to navigate through large groups</li>
                    <li>Click on an article number or subject to read the full content</li>
                </ul>

                <h6>Article Features:</h6>
                <ul>
                    <li><strong>Full Headers:</strong> View complete message headers</li>
                    <li><strong>Threading:</strong> See replies and message relationships</li>
                    <li><strong>Navigation:</strong> Move between previous/next articles</li>
                    <li><strong>Cross-posting:</strong> Articles posted to multiple groups</li>
                </ul>
            </div>
        </div>

        <div class="card mb-4">
            <div class="card-header">
                <h5>Search Features</h5>
            </div>
            <div class="card-body">
                <h6>Search Operators:</h6>
                <ul>
                    <li><strong>Phrase search:</strong> Use quotes "exact phrase"</li>
                    <li><strong>Wildcard:</strong> Use * for partial matches</li>
                    <li><strong>Boolean:</strong> AND, OR, NOT operators</li>
                    <li><strong>Group filtering:</strong> Limit search to specific groups</li>
                </ul>

                <h6>Search Tips:</h6>
                <ul>
                    <li>Be specific with your search terms</li>
                    <li>Use quotes for exact phrases</li>
                    <li>Try different spellings or synonyms</li>
                    <li>Filter by group if you know the topic area</li>
                </ul>
            </div>
        </div>

        <div class="card">
            <div class="card-header">
                <h5>Technical Information</h5>
            </div>
            <div class="card-body">
                <h6>About NNTP:</h6>
                <p>NNTP (Network News Transfer Protocol) is a protocol for distributing and retrieving messages in newsgroups. Newsgroups are discussion forums organized by topic.</p>

                <h6>Message Format:</h6>
                <ul>
                    <li><strong>Headers:</strong> Contains metadata like From, Date, Subject</li>
                    <li><strong>Body:</strong> The actual message content</li>
                    <li><strong>References:</strong> Links to previous messages in a thread</li>
                    <li><strong>Message-ID:</strong> Unique identifier for each message</li>
                </ul>

                <h6>Group Hierarchy:</h6>
                <p>Groups are organized in a hierarchical structure using dots, like:</p>
                <ul>
                    <li><code>comp.lang.python</code> - Computer languages, Python</li>
                    <li><code>rec.arts.movies</code> - Recreation, arts, movies</li>
                    <li><code>sci.physics</code> - Science, physics</li>
                </ul>
            </div>
        </div>
    </div>

    <div class="col-lg-4">
        <div class="card">
            <div class="card-header">
                <h5>Quick Links</h5>
            </div>
            <div class="card-body">
                <div class="list-group list-group-flush">
                    <a href="/" class="list-group-item list-group-item-action">
                        🏠 Home - Browse Sections
                    </a>
                    <a href="/groups" class="list-group-item list-group-item-action">
                        📋 All Groups
                    </a>
                    <a href="/search" class="list-group-item list-group-item-action">
                        🔍 Search Articles
                    </a>
                    <a href="/stats" class="list-group-item list-group-item-action">
                        📊 System Statistics
                    </a>
                </div>
            </div>
        </div>

        <div class="card mt-3">
            <div class="card-header">
                <h6>Keyboard Shortcuts</h6>
            </div>
            <div class="card-body">
                <small>
                    <ul class="list-unstyled">
                        <li><kbd>Ctrl</kbd> + <kbd>F</kbd> - Search page</li>
                        <li><kbd>Alt</kbd> + <kbd>←</kbd> - Browser back</li>
                        <li><kbd>Alt</kbd> + <kbd>→</kbd> - Browser forward</li>
                        <li><kbd>/</kbd> - Focus search (on search page)</li>
                    </ul>
                </small>
            </div>
        </div>

        <div class="card mt-3">
            <div class="card-header">
                <h6>About</h6>
            </div>
            <div class="card-body">
                <p><strong>PugLeaf</strong></p>
                <small class="text-muted">
                    A modern PHP frontend for the go-pugleaf NNTP server.
                    Built with clean architecture and modern security practices.
                </small>

                <hr>

                <p><strong>Backend:</strong> <small>go-pugleaf</small></p>
                <p><strong>Frontend:</strong> <small>PHP 7.4+ with Bootstrap 5</small></p>

                <a href="https://github.com/go-while/go-pugleaf" target="_blank" class="btn btn-sm btn-outline-secondary">
                    View on GitHub
                </a>
            </div>
        </div>
    </div>
</div>
