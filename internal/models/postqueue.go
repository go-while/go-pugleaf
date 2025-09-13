package models

// PostQueueChannel is the global channel for articles posted from web interface
// This channel is used to pass articles from the web interface to the processor
// for background processing and insertion into the NNTP system
var PostQueueChannel = make(chan *Article, 100)
