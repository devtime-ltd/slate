package templates

import "embed"

//go:embed laravel/*
var Laravel embed.FS

//go:embed nextjs/*
var NextJS embed.FS
