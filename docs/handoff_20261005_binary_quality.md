# Binary quality labels

The artifact API now interprets exactly one quality label per artifact: testing,
stable, or disabled. Installer remains an independent artifact-kind tag. New
registrations without a quality label default to testing. Legacy combined labels
are interpreted with disabled taking precedence, then stable, then testing.

Memory and PostgreSQL selectors exclude disabled artifacts from every download,
even explicit-version and disabled-tag requests. Matching testing excludes legacy
stable+testing artifacts. Other stable versions remain eligible after disabling
a newer version.

Existing public resolver routes accept version plus metadata=1 to read exact
artifact identity and status without signing a download URL. This includes disabled
versions. Requests with current_version receive current_status and warning when
the installed version is disabled. If no candidate exists, a disabled installed
version produces HTTP 410 with an upgrade recommendation.

The release controller updates metadata through the existing authenticated admin
artifact endpoint. No new write privilege, binary upload, generated transport file,
or database migration is required. Domain quality normalization is shared with
storage; HTTP handlers retain signing and response construction.

Deploy both regional Managers before the dependent Release controller. Updated
paxd/paxl binaries display installed-version warnings during update checks. Existing
signed URLs may work until expiry; local executables are not revoked or stopped.

Validation: Manager unit suite; fmt-check and lint; isolated PostgreSQL and memory
selection contract; metadata, legacy-tag, and installed-version warning HTTP tests.
No existing local issue was closed by this change.
